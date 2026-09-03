package main

import (
	"os"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/machine"
)

func withRegistryDir(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	prev := machine.Dir()
	machine.SetDir(dir)
	return func() { machine.SetDir(prev) }
}

func TestDoRegisterAndLookup(t *testing.T) {
	defer withRegistryDir(t)()

	if err := doRegister([]string{"--class=container", "--service=alpha-svc", "--root=/mnt/alpha", "alpha", "4242"}); err != nil {
		t.Fatal(err)
	}
	m, err := machine.Lookup("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.PID != 4242 || m.Class != "container" || m.Service != "alpha-svc" || m.Root != "/mnt/alpha" {
		t.Errorf("register roundtrip lost data: %+v", m)
	}
}

func TestDoRegisterRejectsBadArgs(t *testing.T) {
	defer withRegistryDir(t)()

	// Wrong arity
	if err := doRegister([]string{"only-name"}); err == nil {
		t.Error("register with 1 arg should fail")
	}
	// Non-numeric PID
	if err := doRegister([]string{"name", "abc"}); err == nil {
		t.Error("register with non-numeric PID should fail")
	}
	// Invalid name (bubbles up from pkg/machine)
	if err := doRegister([]string{"bad/name", "1234"}); err == nil {
		t.Error("register with slash-name should fail")
	}
}

func TestDoUnregisterRoundtrip(t *testing.T) {
	defer withRegistryDir(t)()

	doRegister([]string{"beta", "500"})
	if err := doUnregister([]string{"beta"}); err != nil {
		t.Fatal(err)
	}
	m, _ := machine.Lookup("beta")
	if m != nil {
		t.Errorf("unregister left entry: %+v", m)
	}
}

func TestDoStatusReportsMissing(t *testing.T) {
	defer withRegistryDir(t)()

	err := doStatus([]string{"nobody"})
	if err == nil || !strings.Contains(err.Error(), "no such machine") {
		t.Errorf("status on unknown should error with 'no such machine', got %v", err)
	}
}

func TestDoListEmpty(t *testing.T) {
	defer withRegistryDir(t)()

	// Capture stdout: replace with a pipe.
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := doList(nil)
	w.Close()
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, _ := r.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, "no machines registered") {
		t.Errorf("empty list should say '(no machines registered)', got %q", got)
	}
}

func TestDoListPopulated(t *testing.T) {
	defer withRegistryDir(t)()

	doRegister([]string{"--class=container", "one", "10"})
	doRegister([]string{"--service=my-svc", "two", "20"})

	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := doList(nil)
	w.Close()
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	got := string(buf[:n])
	for _, want := range []string{"NAME", "one", "10", "two", "20", "container", "my-svc"} {
		if !strings.Contains(got, want) {
			t.Errorf("list output missing %q; got:\n%s", want, got)
		}
	}
}
