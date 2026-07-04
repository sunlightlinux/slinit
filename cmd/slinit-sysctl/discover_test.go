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
	lib := filepath.Join(root, "usr", "lib", "sysctl.d")
	etc := filepath.Join(root, "etc", "sysctl.d")
	mkFile(t, lib, "shared.conf", "vm.swappiness=10")
	mkFile(t, etc, "shared.conf", "vm.swappiness=60")
	mkFile(t, etc, "local.conf", "net.ipv4.ip_forward=1")

	got := discover([]string{lib, etc}, "")
	if len(got) != 2 {
		t.Fatalf("count=%d, want 2", len(got))
	}
	// Both under /etc/sysctl.d/ — shared.conf comes from /etc/, not /usr/lib/.
	if !strings.HasSuffix(got[0], "/etc/sysctl.d/local.conf") {
		t.Errorf("got[0]=%q", got[0])
	}
	if !strings.HasSuffix(got[1], "/etc/sysctl.d/shared.conf") {
		t.Errorf("got[1]=%q (etc should override usr/lib)", got[1])
	}
}

func TestDiscoverAppendsLegacyLast(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc", "sysctl.d")
	mkFile(t, etc, "a.conf", "kernel.pid_max=32768")
	legacy := filepath.Join(root, "etc", "sysctl.conf")
	if err := os.WriteFile(legacy, []byte("legacy=1"), 0644); err != nil {
		t.Fatal(err)
	}
	got := discover([]string{etc}, legacy)
	if len(got) != 2 {
		t.Fatalf("count=%d", len(got))
	}
	if !strings.HasSuffix(got[len(got)-1], "sysctl.conf") {
		t.Errorf("legacy not last: %v", got)
	}
}

func TestDiscoverMissingLegacyIsSkipped(t *testing.T) {
	got := discover(nil, "/nonexistent/sysctl.conf")
	if len(got) != 0 {
		t.Errorf("got=%v", got)
	}
}

func TestDiscoverSkipsNonConf(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	mkFile(t, dir, "wanted.conf", "x=1")
	mkFile(t, dir, "notes.txt", "not a sysctl")
	mkFile(t, dir, "wanted.bak", "backup")
	got := discover([]string{dir}, "")
	if len(got) != 1 {
		t.Errorf("count=%d, want 1: %v", len(got), got)
	}
}
