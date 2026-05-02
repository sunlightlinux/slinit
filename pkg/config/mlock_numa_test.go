package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

func TestParseMlockallFlags(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"current", unix.MCL_CURRENT},
		{"future", unix.MCL_FUTURE},
		{"both", unix.MCL_CURRENT | unix.MCL_FUTURE},
		{"yes", unix.MCL_CURRENT | unix.MCL_FUTURE},
		{"current+future", unix.MCL_CURRENT | unix.MCL_FUTURE},
		{"current,future,onfault", unix.MCL_CURRENT | unix.MCL_FUTURE | unix.MCL_ONFAULT},
		{"no", 0},
	}
	for _, c := range cases {
		got, err := parseMlockallFlags(c.input)
		if err != nil {
			t.Errorf("parseMlockallFlags(%q): %v", c.input, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseMlockallFlags(%q) = %d, want %d", c.input, got, c.want)
		}
	}
	for _, bad := range []string{"", "nonsense", "current+garbage"} {
		if _, err := parseMlockallFlags(bad); err == nil {
			t.Errorf("parseMlockallFlags(%q): expected error", bad)
		}
	}
}

func TestParseMempolicyMode(t *testing.T) {
	cases := map[string]uint32{
		"default":    unix.MPOL_DEFAULT,
		"bind":       unix.MPOL_BIND,
		"preferred":  unix.MPOL_PREFERRED,
		"interleave": unix.MPOL_INTERLEAVE,
		"local":      unix.MPOL_LOCAL,
	}
	for in, want := range cases {
		got, err := parseMempolicyMode(in)
		if err != nil {
			t.Errorf("parseMempolicyMode(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseMempolicyMode(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parseMempolicyMode("xyzzy"); err == nil {
		t.Error("expected error for unknown mode")
	}
}

func TestNumaBindRequiresNodes(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "numa-bind",
		"type = process\ncommand = /bin/sleep 60\nnuma-mempolicy = bind\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("numa-bind")
	if err == nil {
		t.Fatal("expected error: bind without numa-nodes")
	}
	if !strings.Contains(err.Error(), "numa-nodes") {
		t.Errorf("expected 'numa-nodes' in error, got: %v", err)
	}
}

func TestNumaLocalRejectsNodes(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "numa-local",
		"type = process\ncommand = /bin/sleep 60\nnuma-mempolicy = local\nnuma-nodes = 0,1\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("numa-local")
	if err == nil {
		t.Fatal("expected error: local with explicit numa-nodes")
	}
}

func TestNumaNodesWithoutMempolicyRejected(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "stray",
		"type = process\ncommand = /bin/sleep 60\nnuma-nodes = 0-3\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("stray")
	if err == nil {
		t.Fatal("expected error: numa-nodes without mempolicy")
	}
}

func TestNumaInterleaveLoads(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "numa-il",
		"type = process\n"+
			"command = /bin/sleep 60\n"+
			"numa-mempolicy = interleave\n"+
			"numa-nodes = 0-3\n"+
			"mlockall = both\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	if _, err := loader.LoadService("numa-il"); err != nil {
		t.Fatalf("LoadService: %v", err)
	}
}
