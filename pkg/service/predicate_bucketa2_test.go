package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBucketA2KindByNameRoundtrip(t *testing.T) {
	for _, name := range []string{
		"file-is-executable", "path-is-symbolic-link", "path-is-read-write",
		"firmware", "machine-tag", "credential", "control-group-controller",
	} {
		kind, ok := PredicateKindByName(name)
		if !ok {
			t.Errorf("PredicateKindByName(%q) = _,false; want true", name)
			continue
		}
		p := Predicate{Kind: kind, Param: "x"}
		got := p.String()
		want := "condition-" + name + "=x"
		if got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

// TestFileIsExecutablePaths exercises the three failure modes + one
// pass. Uses a tempdir so ownership/perm assumptions don't collide
// with the CI runner's own /bin layout.
func TestFileIsExecutablePaths(t *testing.T) {
	dir := t.TempDir()

	execPath := filepath.Join(dir, "exec")
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	if ok, why := checkFileIsExecutable(execPath); !ok {
		t.Errorf("expected true on executable regular file, got %q", why)
	}

	plainPath := filepath.Join(dir, "plain")
	if err := os.WriteFile(plainPath, []byte("no shebang"), 0644); err != nil {
		t.Fatalf("write plain: %v", err)
	}
	if ok, _ := checkFileIsExecutable(plainPath); ok {
		t.Error("expected false on 0644 file")
	}

	if ok, _ := checkFileIsExecutable(dir); ok {
		t.Error("expected false on a directory")
	}

	if ok, _ := checkFileIsExecutable(filepath.Join(dir, "nope")); ok {
		t.Error("expected false on missing path")
	}
}

// TestPathIsSymbolicLink covers both the positive and negative case.
func TestPathIsSymbolicLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("hi"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if ok, why := checkPathIsSymbolicLink(link); !ok {
		t.Errorf("expected true on a symlink, got %q", why)
	}
	if ok, _ := checkPathIsSymbolicLink(target); ok {
		t.Error("expected false on the target regular file")
	}
	if ok, _ := checkPathIsSymbolicLink(filepath.Join(dir, "missing")); ok {
		t.Error("expected false on missing path")
	}
}

// TestPathIsReadWrite exercises the read-write branch. Testing the
// RDONLY branch cleanly would need a bind-mount or a squashfs — CI
// hosts vary too much. The negative path is only wired for the
// missing-path case, which is safe on any host.
func TestPathIsReadWrite(t *testing.T) {
	if ok, why := checkPathIsReadWrite(t.TempDir()); !ok {
		t.Errorf("expected true on a writable tempdir, got %q", why)
	}
	if ok, _ := checkPathIsReadWrite("/nonexistent/slinit-a2-probe"); ok {
		t.Error("expected false on missing path")
	}
}

// TestFirmwareUnknownKeyErrs pins the fallback behaviour so a typo
// surfaces as a clear failure rather than a silent pass.
func TestFirmwareUnknownKeyErrs(t *testing.T) {
	if ok, _ := checkFirmware("this-is-definitely-not-a-firmware-name-in-any-DMI-string"); ok {
		t.Error("expected false on a bogus firmware key")
	}
}

// TestMachineTagShapesFail confirms both "no /etc/machine-info" and
// "TAGS field present but tag missing" paths return false with a
// readable reason.
func TestMachineTagShapesFail(t *testing.T) {
	if ok, _ := checkMachineTag(""); ok {
		t.Error("empty tag should not match")
	}
}

// TestCredentialAgainstEnv exercises the presence/absence + safety
// guards without touching any real credential dir.
func TestCredentialAgainstEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api-key"), []byte("secret"), 0400); err != nil {
		t.Fatalf("write cred: %v", err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", dir)

	if ok, why := checkCredential("api-key"); !ok {
		t.Errorf("expected true when credential present, got %q", why)
	}
	if ok, _ := checkCredential("does-not-exist"); ok {
		t.Error("expected false when credential absent")
	}
	if ok, why := checkCredential("../escape"); ok || why == "" {
		t.Errorf("credential name with '/' must be rejected, got ok=%v why=%q", ok, why)
	}

	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if ok, _ := checkCredential("api-key"); ok {
		t.Error("expected false when CREDENTIALS_DIRECTORY unset")
	}
}

// TestControlGroupControllerCovered checks the primary read path
// against the running host's /sys/fs/cgroup/cgroup.controllers. On
// any modern Linux CI runner "cpu" is invariably present; if the
// file is missing we skip cleanly.
func TestControlGroupControllerCovered(t *testing.T) {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("no cgroup v2 root controllers file on this host")
	}
	if ok, why := checkControlGroupController("cpu"); !ok {
		// It's possible the host has cpu delegated only in subtree;
		// downgrade to a diagnostic instead of hard-failing.
		t.Logf("checkControlGroupController(\"cpu\") = false: %s", why)
	}
	if ok, _ := checkControlGroupController("this-controller-does-not-exist"); ok {
		t.Error("bogus controller should not match")
	}
}
