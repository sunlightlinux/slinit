package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSvc(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// The link must land where the loader looks: the source's first
// waits-for.d entry, resolved against the source file's directory —
// the same place the daemon's enable writes it.
func TestOfflineEnableDisableUsesWaitsForD(t *testing.T) {
	dir := t.TempDir()
	writeSvc(t, dir, "boot", "type = internal\nwaits-for.d: boot.d\n")
	writeSvc(t, dir, "nginx", "type = process\ncommand = /bin/true\n")

	if err := offlineEnable(dir, "", "nginx"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	link := filepath.Join(dir, "boot.d", "nginx")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("link not created: %v", err)
	}
	if target != "../nginx" {
		t.Errorf("target = %q, want ../nginx", target)
	}
	// Idempotent.
	if err := offlineEnable(dir, "", "nginx"); err != nil {
		t.Fatalf("second enable: %v", err)
	}

	if err := offlineDisable(dir, "", "nginx"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("link still present after disable: %v", err)
	}
	// Disabling again is not an error.
	if err := offlineDisable(dir, "", "nginx"); err != nil {
		t.Fatalf("second disable: %v", err)
	}
}

func TestOfflineEnableAbsoluteWaitsForD(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(t.TempDir(), "wants")
	writeSvc(t, dir, "boot", "type = internal\nwaits-for.d: "+abs+"\n")
	writeSvc(t, dir, "nginx", "type = process\ncommand = /bin/true\n")

	if err := offlineEnable(dir, "", "nginx"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(abs, "nginx")); err != nil {
		t.Errorf("link not in absolute waits-for.d: %v", err)
	}
}

func TestOfflineEnableHonoursEnableViaAndFrom(t *testing.T) {
	dir := t.TempDir()
	writeSvc(t, dir, "boot", "type = internal\nwaits-for.d: boot.d\n")
	writeSvc(t, dir, "mygroup", "type = internal\nwaits-for.d: mygroup.d\n")
	writeSvc(t, dir, "net", "type = internal\nwaits-for.d: net.d\n")
	writeSvc(t, dir, "opt", "type = process\ncommand = /bin/true\n@meta enable-via mygroup\n")

	if err := offlineEnable(dir, "", "opt"); err != nil {
		t.Fatalf("enable via @meta: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "mygroup.d", "opt")); err != nil {
		t.Errorf("enable-via ignored: %v", err)
	}

	// An explicit --from wins over enable-via.
	if err := offlineEnable(dir, "net", "opt"); err != nil {
		t.Fatalf("enable --from: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "net.d", "opt")); err != nil {
		t.Errorf("--from ignored: %v", err)
	}
}

func TestOfflineEnableTemplateInstance(t *testing.T) {
	dir := t.TempDir()
	writeSvc(t, dir, "boot", "type = internal\nwaits-for.d: boot.d\n")
	writeSvc(t, dir, "getty", "type = process\ncommand = /sbin/agetty $1\n")

	if err := offlineEnable(dir, "", "getty@tty1"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "boot.d", "getty@tty1")); err != nil {
		t.Errorf("instance link missing: %v", err)
	}
}

func TestOfflineEnableErrors(t *testing.T) {
	dir := t.TempDir()
	writeSvc(t, dir, "nginx", "type = process\ncommand = /bin/true\n")

	// No source service file at all.
	err := offlineEnable(dir, "", "nginx")
	if err == nil || !strings.Contains(err.Error(), "boot") {
		t.Errorf("missing source: err = %v", err)
	}

	// Source without waits-for.d: a link anywhere else would never be
	// read by the loader, so refuse rather than write a dead link.
	writeSvc(t, dir, "boot", "type = internal\n")
	err = offlineEnable(dir, "", "nginx")
	if err == nil || !strings.Contains(err.Error(), "waits-for.d") {
		t.Errorf("no waits-for.d: err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "waits-for.d")); !os.IsNotExist(statErr) {
		t.Errorf("a waits-for.d directory was created anyway")
	}

	// Missing target service.
	writeSvc(t, dir, "boot", "type = internal\nwaits-for.d: boot.d\n")
	if err := offlineEnable(dir, "", "nosuch"); err == nil {
		t.Errorf("missing target: expected an error")
	}
}

// A service whose file was deleted must still be disableable, so its
// dangling link does not break the next boot's load.
func TestOfflineDisableAfterTargetRemoved(t *testing.T) {
	dir := t.TempDir()
	writeSvc(t, dir, "boot", "type = internal\nwaits-for.d: boot.d\n")
	writeSvc(t, dir, "nginx", "type = process\ncommand = /bin/true\n")
	if err := offlineEnable(dir, "", "nginx"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "nginx")); err != nil {
		t.Fatal(err)
	}
	if err := offlineDisable(dir, "", "nginx"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "boot.d", "nginx")); !os.IsNotExist(err) {
		t.Errorf("dangling link left behind: %v", err)
	}
}
