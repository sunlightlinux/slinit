package machine

import (
	"os"
	"path/filepath"
	"testing"
)

func withTempDir(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	prev := currentDir
	SetDir(dir)
	return func() { SetDir(prev) }
}

func TestRegisterLookupRoundtrip(t *testing.T) {
	defer withTempDir(t)()

	m := Machine{
		Name:    "alpha",
		PID:     4242,
		Class:   "container",
		Service: "alpha-container",
		Root:    "/var/lib/machines/alpha",
	}
	if err := Register(m); err != nil {
		t.Fatal(err)
	}
	got, err := Lookup("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("Lookup returned nil after Register")
	}
	if got.PID != m.PID || got.Class != m.Class || got.Service != m.Service || got.Root != m.Root {
		t.Errorf("roundtrip lost data: got %+v want %+v", got, m)
	}
}

func TestLookupUnknownReturnsNilNil(t *testing.T) {
	defer withTempDir(t)()

	got, err := Lookup("nobody")
	if got != nil || err != nil {
		t.Errorf("Lookup(unknown) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestRegisterAtomicOverwrite(t *testing.T) {
	defer withTempDir(t)()

	if err := Register(Machine{Name: "beta", PID: 100}); err != nil {
		t.Fatal(err)
	}
	if err := Register(Machine{Name: "beta", PID: 200, Class: "vm"}); err != nil {
		t.Fatal(err)
	}
	got, _ := Lookup("beta")
	if got.PID != 200 || got.Class != "vm" {
		t.Errorf("overwrite lost: got %+v", got)
	}
	// No .tmp leaked
	files, _ := os.ReadDir(Dir())
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".tmp" {
			t.Errorf("leaked temp file: %s", f.Name())
		}
	}
}

func TestUnregisterRemoves(t *testing.T) {
	defer withTempDir(t)()

	Register(Machine{Name: "gamma", PID: 300})
	if err := Unregister("gamma"); err != nil {
		t.Fatal(err)
	}
	got, _ := Lookup("gamma")
	if got != nil {
		t.Errorf("Lookup after Unregister returned %+v", got)
	}
}

func TestUnregisterMissingIsNoError(t *testing.T) {
	defer withTempDir(t)()

	if err := Unregister("never-existed"); err != nil {
		t.Errorf("Unregister(missing) returned %v, want nil", err)
	}
}

func TestListReturnsAll(t *testing.T) {
	defer withTempDir(t)()

	Register(Machine{Name: "one", PID: 10})
	Register(Machine{Name: "two", PID: 20})
	Register(Machine{Name: "three", PID: 30})

	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d entries, want 3", len(got))
	}
}

func TestListSkipsTempFiles(t *testing.T) {
	defer withTempDir(t)()

	Register(Machine{Name: "real", PID: 42})
	// Simulate a mid-write tmp file
	os.WriteFile(filepath.Join(Dir(), "leaked.tmp"), []byte("999\n"), 0o644)

	got, _ := List()
	for _, m := range got {
		if m.Name == "leaked.tmp" {
			t.Errorf(".tmp file surfaced in List: %+v", m)
		}
	}
}

func TestRegisterRejectsInvalidNames(t *testing.T) {
	defer withTempDir(t)()

	for _, bad := range []string{"", ".hidden", "-dashy", "with/slash", "with space"} {
		if err := Register(Machine{Name: bad, PID: 100}); err == nil {
			t.Errorf("Register(%q) accepted; want rejection", bad)
		}
	}
}

func TestRegisterRejectsPathTraversal(t *testing.T) {
	defer withTempDir(t)()

	// Name-validation blocks "/" so ../ escape can't form. Explicit
	// check keeps the invariant asserted rather than only implied.
	for _, bad := range []string{"../escape", "sub/deep", "a/b"} {
		if err := Register(Machine{Name: bad, PID: 100}); err == nil {
			t.Errorf("Register(%q) accepted — path traversal risk", bad)
		}
	}
}

func TestRegisterRejectsBogusPID(t *testing.T) {
	defer withTempDir(t)()

	for _, bad := range []int{0, 1, -1, -99} {
		if err := Register(Machine{Name: "x", PID: bad}); err == nil {
			t.Errorf("Register PID=%d accepted; want rejection", bad)
		}
	}
}

func TestAliveOnSelfAndBogus(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("Alive(self) returned false")
	}
	// PID 1 is filtered as never-a-container-PID.
	if Alive(1) {
		t.Error("Alive(1) returned true; expected filtered")
	}
	// A very-high PID that almost certainly doesn't exist.
	if Alive(2_000_000_000) {
		t.Error("Alive(giant) returned true; expected false")
	}
}
