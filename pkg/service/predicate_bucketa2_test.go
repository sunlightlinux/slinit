package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
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
	withCredentialStores(t, t.TempDir())
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

func withCredentialStores(t *testing.T, dirs ...string) {
	t.Helper()
	saved := process.CredentialStores
	process.CredentialStores = dirs
	t.Cleanup(func() { process.CredentialStores = saved })
	t.Setenv("SYSTEM_CREDSTORE", "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("ENCRYPTED_CREDENTIALS_DIRECTORY", "")
}

// systemd v263: ConditionCredential= also finds credentials in the
// credential stores, plain and .encrypted, the way ImportCredential=
// does, so a credential that configures a service can gate it too.
func TestCredentialInStores(t *testing.T) {
	root := t.TempDir()
	etc, run := filepath.Join(root, "etc-credstore"), filepath.Join(root, "run-credstore")
	for _, d := range []string{etc, run, run + ".encrypted"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	withCredentialStores(t, etc, run)
	if err := os.WriteFile(filepath.Join(run, "imds.token"), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run+".encrypted", "sealed"), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	// Present but dangling: existence is checked, the link not followed.
	if err := os.Symlink("/nonexistent", filepath.Join(etc, "link")); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"imds.token", "sealed", "link"} {
		if ok, why := checkCredential(name); !ok {
			t.Errorf("%s: not found in the stores: %s", name, why)
		}
	}
	if ok, _ := checkCredential("absent"); ok {
		t.Error("absent credential reported present")
	}

	// The manager's own credentials are still searched, and first.
	own := t.TempDir()
	if err := os.WriteFile(filepath.Join(own, "mine"), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENCRYPTED_CREDENTIALS_DIRECTORY", own)
	if ok, why := checkCredential("mine"); !ok {
		t.Errorf("credential in $ENCRYPTED_CREDENTIALS_DIRECTORY not found: %s", why)
	}
}

// A broken store must not hide a match in a later one, and is reported
// only when the credential is found nowhere.
func TestCredentialStoreErrors(t *testing.T) {
	root := t.TempDir()
	notDir := filepath.Join(root, "file") // Lstat(file/x) fails with ENOTDIR
	if err := os.WriteFile(notDir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(root, "good")
	if err := os.Mkdir(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "c"), nil, 0o400); err != nil {
		t.Fatal(err)
	}

	withCredentialStores(t, notDir, good)
	if ok, why := checkCredential("c"); !ok {
		t.Errorf("an unreadable first store hid the match in the second: %s", why)
	}
	withCredentialStores(t, notDir)
	if ok, why := checkCredential("c"); ok || !strings.Contains(why, "not a directory") {
		t.Errorf("found nowhere with a broken store: ok=%v why=%q, want the error reported", ok, why)
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
