package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func hasEdge(svc service.Service, to string, dt service.DependencyType) bool {
	for _, d := range svc.Record().Dependencies() {
		if d.To.Name() == to && d.DepType == dt {
			return true
		}
	}
	return false
}

// `a: before: x` is the edge x→a, held by x, which waits for a: a starts
// first. The loader put it on a, so a waited for x. A reload keeps the
// edge on x, and removes it when the description drops the line.
func TestBeforeIsHeldByItsTarget(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "x", "type = internal\n")
	writeServiceFile(t, dir, "a", "type = internal\nbefore: x\n")

	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{dir})
	ss.SetLoader(dl)
	a, err := dl.LoadService("a")
	if err != nil {
		t.Fatal(err)
	}
	x := ss.FindService("x", false)
	if !hasEdge(x, "a", service.DepBefore) || hasEdge(a, "x", service.DepBefore) {
		t.Fatalf("a: before: x should be the edge x→a; a has %v, x has %v",
			a.Record().Dependencies(), x.Record().Dependencies())
	}

	// Unchanged reload: the fast path must see the edge as a's.
	if _, err := dl.ReloadService(a); err != nil {
		t.Fatal(err)
	}
	if n := countEdges(x, "a", service.DepBefore); n != 1 {
		t.Fatalf("after an unchanged reload x holds %d before edges to a, want 1", n)
	}

	// The line removed: the edge goes.
	os.WriteFile(filepath.Join(dir, "a"), []byte("type = internal\n"), 0o644)
	if _, err := dl.ReloadService(a); err != nil {
		t.Fatal(err)
	}
	if hasEdge(x, "a", service.DepBefore) {
		t.Fatal("before: removed from a, but x still holds the edge")
	}

	// A type change keeps both kinds of BEFORE edge: the one a declares
	// (held by x) and one another service declared on a (held by a).
	writeServiceFile(t, dir, "y", "type = internal\nbefore: a\n")
	if _, err := dl.LoadService("y"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a"), []byte("type = scripted\ncommand = /bin/true\nbefore: x\n"), 0o644)
	a2, err := dl.ReloadService(ss.FindService("a", false))
	if err != nil {
		t.Fatal(err)
	}
	if countEdges(x, "a", service.DepBefore) != 1 {
		t.Errorf("after a type change x holds %d before edges to a, want 1", countEdges(x, "a", service.DepBefore))
	}
	if !hasEdge(a2, "y", service.DepBefore) {
		t.Errorf("y: before: a was lost when a changed type; a has %v", a2.Record().Dependencies())
	}
}

func countEdges(svc service.Service, to string, dt service.DependencyType) int {
	n := 0
	for _, d := range svc.Record().Dependencies() {
		if d.To.Name() == to && d.DepType == dt {
			n++
		}
	}
	return n
}

// A before: whose target this service already waits for is a cycle: the
// target would wait for a service that waits for it. Found by review:
// with the edge stored on its target and no check, a, b and x sat in
// STARTING for good.
func TestBeforeCycleIsRefused(t *testing.T) {
	load := func(t *testing.T, files map[string]string, name string) error {
		dir := t.TempDir()
		for n, body := range files {
			writeServiceFile(t, dir, n, body)
		}
		ss := service.NewServiceSet(&testConsumerLogger{})
		dl := NewDirLoader(ss, []string{dir})
		ss.SetLoader(dl)
		_, err := dl.LoadService(name)
		return err
	}
	t.Run("through a dependency", func(t *testing.T) {
		err := load(t, map[string]string{
			"x": "type = internal\n",
			"b": "type = internal\ndepends-on: x\n",
			"a": "type = internal\nwaits-for: b\nbefore: x\n",
		}, "a")
		if err == nil || !strings.Contains(err.Error(), "already waits for x") {
			t.Fatalf("a waits for x through b and says before: x; want a cycle error, got %v", err)
		}
	})
	t.Run("directly", func(t *testing.T) {
		if err := load(t, map[string]string{
			"x": "type = internal\n",
			"a": "type = internal\ndepends-on: x\nbefore: x\n",
		}, "a"); err == nil {
			t.Fatal("depends-on: x with before: x loaded")
		}
	})
	t.Run("itself", func(t *testing.T) {
		if err := load(t, map[string]string{"a": "type = internal\nbefore: a\n"}, "a"); err == nil {
			t.Fatal("before: a on a loaded")
		}
	})
	t.Run("the same order said from both ends loads", func(t *testing.T) {
		if err := load(t, map[string]string{
			"x": "type = internal\nafter: a\n",
			"a": "type = internal\nbefore: x\n",
		}, "x"); err != nil {
			t.Fatalf("x: after: a with a: before: x is no cycle: %v", err)
		}
	})
}

// The target's depth follows the edge put on it.
func TestBeforeUpdatesTheTargetsDepth(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "x", "type = internal\n")
	writeServiceFile(t, dir, "c", "type = internal\n")
	writeServiceFile(t, dir, "b", "type = internal\ndepends-on: c\n")
	writeServiceFile(t, dir, "a", "type = internal\ndepends-on: b\nbefore: x\n")
	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{dir})
	ss.SetLoader(dl)
	if _, err := dl.LoadService("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := dl.LoadService("a"); err != nil {
		t.Fatal(err)
	}
	if d := ss.FindService("x", false).Record().DepDepth(); d != 3 {
		t.Errorf("x waits for a (depth 2), so its depth is 3, got %d", d)
	}
}
