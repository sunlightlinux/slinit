package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiscoverLateWinsOverEarly(t *testing.T) {
	root := t.TempDir()
	lib := filepath.Join(root, "usr", "lib", "binfmt.d")
	etc := filepath.Join(root, "etc", "binfmt.d")
	mkFile(t, lib, "shared.conf", "distro")
	mkFile(t, etc, "shared.conf", "operator")
	mkFile(t, etc, "local.conf", "only-in-etc")

	got := discover([]string{lib, etc})
	if len(got) != 2 {
		t.Fatalf("count=%d, want 2", len(got))
	}
	// Deterministic alphabetical order → local.conf first, then shared.
	if !strings.HasSuffix(got[0], "/etc/binfmt.d/local.conf") {
		t.Errorf("got[0]=%q", got[0])
	}
	if !strings.HasSuffix(got[1], "/etc/binfmt.d/shared.conf") {
		t.Errorf("got[1]=%q (expected /etc/ variant to win)", got[1])
	}
}

func TestDiscoverSkipsNonConf(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "d")
	mkFile(t, dir, "wanted.conf", "x")
	mkFile(t, dir, "README", "readme")
	mkFile(t, dir, "wanted.bak", "backup")

	got := discover([]string{dir})
	if len(got) != 1 {
		t.Fatalf("count=%d, want 1", len(got))
	}
}

func TestDiscoverMissingDirIsSkipped(t *testing.T) {
	got := discover([]string{"/nonexistent/definitely-not-here-42"})
	if len(got) != 0 {
		t.Errorf("got=%v", got)
	}
}
