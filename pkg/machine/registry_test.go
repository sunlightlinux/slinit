package machine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// No temp file leaked, and the entry is world-readable.
	files, _ := os.ReadDir(Dir())
	for _, f := range files {
		if f.Name() != "beta" {
			t.Errorf("leaked temp file: %s", f.Name())
		}
	}
	if st, err := os.Stat(filepath.Join(Dir(), "beta")); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("entry mode = %v (%v), want 0644", st.Mode().Perm(), err)
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
	// Simulate a mid-write temp file
	os.WriteFile(filepath.Join(Dir(), ".real.12345"), []byte("999\n"), 0o644)

	got, _ := List()
	if len(got) != 1 || got[0].Name != "real" {
		t.Errorf("List = %+v, want only \"real\"", got)
	}
}

// "a.tmp" is a valid machine name: it must be listed, and registering
// "a" must not destroy it (the temp file used to be "a.tmp").
func TestTmpSuffixedNameIsAMachine(t *testing.T) {
	defer withTempDir(t)()

	if err := Register(Machine{Name: "a.tmp", PID: 4242}); err != nil {
		t.Fatal(err)
	}
	if err := Register(Machine{Name: "a", PID: 4343}); err != nil {
		t.Fatal(err)
	}
	m, err := Lookup("a.tmp")
	if err != nil || m == nil || m.PID != 4242 {
		t.Fatalf("Lookup(a.tmp) after Register(a) = %+v, %v; want PID 4242", m, err)
	}
	got, _ := List()
	names := map[string]bool{}
	for _, m := range got {
		names[m.Name] = true
	}
	if !names["a"] || !names["a.tmp"] || len(got) != 2 {
		t.Errorf("List names = %v, want a and a.tmp", names)
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

// rootOwnedPID returns a live process owned by uid 0 other than PID 1,
// or 0 when there is none to use (or the test runs as root, where
// kill(pid, 0) never returns EPERM).
func rootOwnedPID(t *testing.T) int {
	t.Helper()
	if os.Geteuid() == 0 {
		return 0
	}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "Uid:" && f[1] == "0" {
				return pid
			}
		}
	}
	return 0
}

// A container started by root is alive to an unprivileged reader too:
// kill(pid, 0) answers EPERM for it, not ESRCH.
func TestAliveOtherUsersProcess(t *testing.T) {
	pid := rootOwnedPID(t)
	if pid == 0 {
		t.Skip("no root-owned process to probe, or running as root")
	}
	if !Alive(pid) {
		t.Errorf("Alive(%d) (root-owned, EPERM) = false, want true", pid)
	}
}

// A newline in a metadata value would forge a line of its own: a
// --class of "x\nROOT=/etc" made -M read the journal under /etc.
func TestRegisterRejectsControlCharsInFields(t *testing.T) {
	defer withTempDir(t)()

	bad := []Machine{
		{Name: "f1", PID: 4242, Class: "x\nROOT=/etc"},
		{Name: "f2", PID: 4242, Service: "svc\nROOT=/etc"},
		{Name: "f3", PID: 4242, Root: "/srv/a\nCLASS=vm"},
		{Name: "f4", PID: 4242, Root: "/srv/a\r"},
		{Name: "f5", PID: 4242, Class: "a\x00b"},
		{Name: "f6", PID: 4242, Service: "\x1b[2J"},
	}
	for _, m := range bad {
		if err := Register(m); err == nil {
			got, _ := Lookup(m.Name)
			t.Errorf("Register(%+v) accepted; read back %+v", m, got)
		}
	}
	if err := Register(Machine{Name: "ok", PID: 4242, Class: "container", Service: "my svc", Root: "/var/lib/machines/ök"}); err != nil {
		t.Errorf("Register with plain metadata failed: %v", err)
	}
}
