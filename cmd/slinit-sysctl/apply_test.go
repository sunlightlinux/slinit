package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupFakeProc drops a scratch /proc/sys tree the tests can write
// into, then repoints procSysRoot at it. Restored on cleanup.
func setupFakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proc", "sys"), 0755); err != nil {
		t.Fatal(err)
	}
	saved := procSysRoot
	procSysRoot = filepath.Join(root, "proc", "sys")
	t.Cleanup(func() { procSysRoot = saved })
	return root
}

// prepKernelKey pre-creates the target file (kernel exposes each
// tunable as a pre-existing file) so applySpec's WriteFile doesn't
// have to create it.
func prepKernelKey(t *testing.T, key string) string {
	t.Helper()
	full := filepath.Join(procSysRoot, key)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestApplySpecWritesValuePlusNewline(t *testing.T) {
	setupFakeProc(t)
	target := prepKernelKey(t, "vm/swappiness")
	s := spec{key: "vm/swappiness", rawKey: "vm.swappiness", value: "60"}
	if err := applySpec(s, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "60\n" {
		t.Errorf("got %q, want %q", got, "60\n")
	}
}

func TestApplySpecIgnoreFlagSwallowsErrorInLooseMode(t *testing.T) {
	setupFakeProc(t)
	// Point at a key we haven't pre-created — WriteFile fails because
	// the parent dir doesn't exist. With ignoreErrors, the error must
	// come back as errIgnored, not a hard failure.
	s := spec{
		key: "nonexistent/tunable", rawKey: "nonexistent.tunable",
		value: "1", ignoreErrors: true,
	}
	err := applySpec(s, false)
	if err == nil {
		t.Fatal("expected errIgnored, got nil")
	}
	if _, ok := err.(errIgnored); !ok {
		t.Errorf("want errIgnored, got %T: %v", err, err)
	}
}

func TestApplySpecStrictOverridesIgnoreFlag(t *testing.T) {
	setupFakeProc(t)
	s := spec{
		key: "nonexistent/tunable", rawKey: "nonexistent.tunable",
		value: "1", ignoreErrors: true,
	}
	err := applySpec(s, true) // strict=true
	if err == nil {
		t.Fatal("expected error under strict")
	}
	if _, ok := err.(errIgnored); ok {
		t.Errorf("strict must not return errIgnored")
	}
}

func TestApplyFilesEndToEnd(t *testing.T) {
	setupFakeProc(t)
	swap := prepKernelKey(t, "vm/swappiness")
	fwd := prepKernelKey(t, "net/ipv4/ip_forward")

	dir := t.TempDir()
	confPath := filepath.Join(dir, "s.conf")
	body := "# comment\nvm.swappiness = 25\nnet.ipv4.ip_forward = 1\n-does.not.exist = 1\n"
	if err := os.WriteFile(confPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	res := applyFiles([]string{confPath}, false, false)
	if res.applied != 2 {
		t.Errorf("applied=%d, want 2", res.applied)
	}
	if res.ignored != 1 {
		t.Errorf("ignored=%d, want 1 (missing target)", res.ignored)
	}
	if len(res.errors) != 0 {
		t.Errorf("unexpected errors: %v", res.errors)
	}
	if got, _ := os.ReadFile(swap); string(got) != "25\n" {
		t.Errorf("swap = %q", got)
	}
	if got, _ := os.ReadFile(fwd); string(got) != "1\n" {
		t.Errorf("fwd = %q", got)
	}
}

func TestApplyFilesReportsParseErrors(t *testing.T) {
	setupFakeProc(t)
	dir := t.TempDir()
	confPath := filepath.Join(dir, "bad.conf")
	// Second line is malformed (no '='). parseFile stops there and
	// returns the error, so the first good line is not applied.
	if err := os.WriteFile(confPath, []byte("vm.swappiness = 10\nno equals\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res := applyFiles([]string{confPath}, false, false)
	if len(res.errors) == 0 {
		t.Errorf("expected errors, got none")
	}
	if res.applied != 0 {
		t.Errorf("applied=%d, want 0 (parse aborted)", res.applied)
	}
}

func TestApplyResultString(t *testing.T) {
	r := &applyResult{applied: 5, ignored: 2, errors: []error{nil, nil}}
	got := r.String()
	if !strings.Contains(got, "applied=5") ||
		!strings.Contains(got, "ignored=2") ||
		!strings.Contains(got, "errors=2") {
		t.Errorf("String()=%q", got)
	}
}
