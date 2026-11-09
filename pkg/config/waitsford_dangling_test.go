package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func loaderForDir(t *testing.T, boot string) (*DirLoader, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "boot.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeServiceFile(t, dir, "boot", boot)
	ss := service.NewServiceSet(&testReloadLogger{})
	l := NewDirLoader(ss, []string{dir})
	ss.SetLoader(l)
	return l, dir
}

// One service's file removed must not cost the machine its boot.
func TestDanglingWaitsForDEntryIsIgnored(t *testing.T) {
	l, dir := loaderForDir(t, "type = internal\nwaits-for.d: boot.d\n")
	writeServiceFile(t, dir, "real", "type = internal\n")
	for name, target := range map[string]string{"ghost": "../nothing", "real": "../real"} {
		if err := os.Symlink(target, filepath.Join(dir, "boot.d", name)); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := l.LoadService("boot")
	if err != nil {
		t.Fatalf("boot failed to load: %v", err)
	}
	var got []string
	for _, d := range svc.Record().Dependencies() {
		got = append(got, d.To.Name())
	}
	if len(got) != 1 || got[0] != "real" {
		t.Errorf("dependencies %v, want only real", got)
	}
}

// Only the entry itself being missing is forgiven.
func TestWaitsForDEntryWithAnotherProblemStillFails(t *testing.T) {
	for _, c := range []struct{ name, entry string }{
		{"syntax error", "type = nonsense\n"},
		{"dependency missing further down", "type = internal\ndepends-on: absent\n"},
	} {
		l, dir := loaderForDir(t, "type = internal\nwaits-for.d: boot.d\n")
		writeServiceFile(t, dir, "broken", c.entry)
		if err := os.Symlink("../broken", filepath.Join(dir, "boot.d", "broken")); err != nil {
			t.Fatal(err)
		}
		if _, err := l.LoadService("boot"); err == nil || !strings.Contains(err.Error(), "broken") {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}
