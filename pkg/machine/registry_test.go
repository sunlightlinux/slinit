package machine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestParseStartTime(t *testing.T) {
	// comm may hold spaces and ')': fields count from the last ')'.
	stat := "4242 (a) b c) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 10"
	got, err := parseStartTime(stat)
	if err != nil || got != 987654 {
		t.Errorf("parseStartTime = %d, %v; want 987654", got, err)
	}
	if _, err := parseStartTime("4242 (x) S 1 2"); err == nil {
		t.Error("short stat accepted")
	}
}

func readEntry(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Dir(), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Register records the process's start time, and the entry then names
// that process: Alive while it runs.
func TestRegisterRecordsStartTime(t *testing.T) {
	defer withTempDir(t)()

	self := os.Getpid()
	want, err := ProcStartTime(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := Register(Machine{Name: "self", PID: self}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readEntry(t, "self"), "STARTTIME="+strconv.FormatUint(want, 10)+"\n") {
		t.Errorf("entry lacks STARTTIME=%d:\n%s", want, readEntry(t, "self"))
	}
	m, _ := Lookup("self")
	if m.StartTime != want {
		t.Errorf("Lookup StartTime = %d, want %d", m.StartTime, want)
	}
	if s := m.State(); s != StateAlive {
		t.Errorf("State = %v, want yes", s)
	}
}

// The PID now belongs to a process that is not the registered one: an
// entry left behind by a SIGKILLed launcher, the PID since reused.
func TestStateReusedPID(t *testing.T) {
	defer withTempDir(t)()

	self := os.Getpid()
	st, err := ProcStartTime(self)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(Dir(), "web"),
		[]byte(strconv.Itoa(self)+"\nSTARTTIME="+strconv.FormatUint(st+1, 10)+"\n"), 0o644)
	m, err := Lookup("web")
	if err != nil {
		t.Fatal(err)
	}
	if s := m.State(); s != StateReused {
		t.Errorf("State = %v, want reused", s)
	}
}

// Entries written before STARTTIME existed are judged by when they
// were written: a process that started later is not the one registered;
// one that started before (a container still running across the
// upgrade) is.
func TestStateLegacyEntry(t *testing.T) {
	defer withTempDir(t)()

	self := os.Getpid()
	p := filepath.Join(Dir(), "old")
	os.WriteFile(p, []byte(strconv.Itoa(self)+"\nCLASS=container\n"), 0o644)
	m, _ := Lookup("old")
	if m.StartTime != 0 {
		t.Fatalf("legacy StartTime = %d", m.StartTime)
	}
	if s := m.State(); s != StateAlive {
		t.Errorf("legacy entry written after the process started: State = %v, want yes", s)
	}

	past := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}
	m, _ = Lookup("old")
	if s := m.State(); s != StateReused {
		t.Errorf("legacy entry written before the process started: State = %v, want reused", s)
	}
}

func TestStateDeadAndHidden(t *testing.T) {
	m := &Machine{Name: "gone", PID: 2_000_000_000, StartTime: 5}
	if s := m.State(); s != StateDead {
		t.Errorf("State(no such pid) = %v, want no", s)
	}

	// /proc hides the process (hidepid=): it exists, identity unknown.
	prev := procDir
	procDir = t.TempDir()
	defer func() { procDir = prev }()
	m = &Machine{Name: "hidden", PID: os.Getpid(), StartTime: 5}
	if s := m.State(); s != StateUnknown {
		t.Errorf("State(hidden pid) = %v, want unknown", s)
	}
}
