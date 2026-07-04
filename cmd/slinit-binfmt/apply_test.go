package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupFakeProcfs stubs out /proc/sys/fs/binfmt_misc under root so
// registerSpec / unregisterAll can be exercised without touching the
// real kernel. Returns the paths the tests may want to inspect.
func setupFakeProcfs(t *testing.T) (root, register, statusDir string) {
	t.Helper()
	root = t.TempDir()
	statusDir = filepath.Join(root, "proc", "sys", "fs", "binfmt_misc")
	if err := os.MkdirAll(statusDir, 0755); err != nil {
		t.Fatal(err)
	}
	register = filepath.Join(statusDir, "register")
	if err := os.WriteFile(register, nil, 0644); err != nil {
		t.Fatal(err)
	}
	// Save + restore the package globals so the test does not leak
	// state into sibling tests.
	savedRegister := registerPath
	savedStatus := binfmtStatusDir
	registerPath = register
	binfmtStatusDir = statusDir
	t.Cleanup(func() {
		registerPath = savedRegister
		binfmtStatusDir = savedStatus
	})
	return root, register, statusDir
}

func TestRegisterSpecWritesLineToKernelEntryPoint(t *testing.T) {
	_, register, statusDir := setupFakeProcfs(t)
	s := spec{name: "foo", line: ":foo:M::AA::/bin/foo:"}

	if err := registerSpec(s); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(register)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != s.line {
		t.Errorf("register contents = %q, want %q", got, s.line)
	}
	// No prior file at binfmt_misc/foo, so no unregister happened.
	if _, err := os.Stat(filepath.Join(statusDir, "foo")); err == nil {
		t.Log("kernel would drop a status file at binfmt_misc/foo; fake procfs doesn't")
	}
}

func TestRegisterSpecUnregistersExistingFirst(t *testing.T) {
	_, register, statusDir := setupFakeProcfs(t)
	// Pretend the kernel already has `foo` registered.
	existing := filepath.Join(statusDir, "foo")
	if err := os.WriteFile(existing, []byte("enabled\ninterpreter /old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	s := spec{name: "foo", line: ":foo:M::BB::/bin/new:"}
	if err := registerSpec(s); err != nil {
		t.Fatal(err)
	}
	// The unregister write leaves "-1" in the status file.
	got, _ := os.ReadFile(existing)
	if string(got) != "-1" {
		t.Errorf("unregister marker = %q, want %q", got, "-1")
	}
	// And the register entry point has the new line.
	gotReg, _ := os.ReadFile(register)
	if string(gotReg) != s.line {
		t.Errorf("register = %q", gotReg)
	}
}

func TestUnregisterAllSkipsRegisterAndStatus(t *testing.T) {
	_, _, statusDir := setupFakeProcfs(t)
	for _, name := range []string{"foo", "bar", "status"} {
		if err := os.WriteFile(filepath.Join(statusDir, name),
			[]byte("enabled"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := unregisterAll()
	if err != nil {
		t.Fatal(err)
	}
	if res.unregistered != 2 {
		t.Errorf("unregistered=%d, want 2 (foo+bar; status skipped)", res.unregistered)
	}
	// foo and bar should have been rewritten to "-1"; status untouched.
	if got, _ := os.ReadFile(filepath.Join(statusDir, "status")); string(got) != "enabled" {
		t.Errorf("status file mutated: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(statusDir, "foo")); string(got) != "-1" {
		t.Errorf("foo marker = %q", got)
	}
}

func TestApplyFilesRegistersEverySpec(t *testing.T) {
	_, register, _ := setupFakeProcfs(t)
	dir := t.TempDir()
	body := ":svc1:M::AA::/bin/svc1:\n:svc2:M::BB::/bin/svc2:\n"
	confPath := filepath.Join(dir, "svcs.conf")
	if err := os.WriteFile(confPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := applyFiles([]string{confPath})
	if err != nil {
		t.Fatal(err)
	}
	if res.registered != 2 || len(res.errors) != 0 {
		t.Errorf("res=%s errors=%v", res, res.errors)
	}
	// Only the LAST write's contents remain in the register file (the
	// fake procfs does not append), which is enough to confirm the
	// path was written to. In the real kernel the writes are consumed
	// per-line.
	got, _ := os.ReadFile(register)
	if !strings.HasPrefix(string(got), ":svc") {
		t.Errorf("register contents = %q", got)
	}
}

func TestApplyFilesReportsParseErrorsInResult(t *testing.T) {
	_, _, _ = setupFakeProcfs(t)
	dir := t.TempDir()
	confPath := filepath.Join(dir, "bad.conf")
	if err := os.WriteFile(confPath, []byte(":ok:M::A::/bin/ok:\nbad-alnum-delim\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := applyFiles([]string{confPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.errors) == 0 {
		t.Errorf("expected at least one error, got none")
	}
	// The good line before the bad one was never processed because
	// parseFile returns on the first malformed line.
	if res.registered != 0 {
		t.Errorf("registered=%d, want 0 (parse aborts before dispatch)", res.registered)
	}
}

func TestBinfmtMountedReflectsRegisterPath(t *testing.T) {
	saved := registerPath
	defer func() { registerPath = saved }()
	registerPath = filepath.Join(t.TempDir(), "no-such")
	if binfmtMounted() {
		t.Errorf("expected false for missing register path")
	}
	dir := t.TempDir()
	registerPath = filepath.Join(dir, "register")
	os.WriteFile(registerPath, nil, 0644)
	if !binfmtMounted() {
		t.Errorf("expected true when register path exists")
	}
}
