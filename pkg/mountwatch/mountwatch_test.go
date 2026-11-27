package mountwatch

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type testLogger struct{}

func (testLogger) Info(string, ...interface{})  {}
func (testLogger) Warn(string, ...interface{})  {}
func (testLogger) Debug(string, ...interface{}) {}

func TestUnescapeMountPath(t *testing.T) {
	// The kernel escapes exactly four characters in mountinfo. A path
	// with a space in it is the realistic one: without this, a
	// registration for "/mnt/my disk" could never match.
	cases := map[string]string{
		"/":                      "/",
		"/mnt/data":              "/mnt/data",
		`/mnt/my\040disk`:        "/mnt/my disk",
		`/mnt/a\011b`:            "/mnt/a\tb",
		`/mnt/a\012b`:            "/mnt/a\nb",
		`/mnt/back\134slash`:     "/mnt/back\\slash",
		`/mnt/two\040\040spaces`: "/mnt/two  spaces",
		`/mnt/not-escaped\999`:   `/mnt/not-escaped\999`,
		`/trailing\`:             `/trailing\`,
	}
	for in, want := range cases {
		if got := unescapeMountPath(in); got != want {
			t.Errorf("unescapeMountPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewSeedsTheKnownSet(t *testing.T) {
	// Seeding matters: without it the first poll would treat every
	// filesystem mounted at boot as newly appeared and fire everything
	// at once.
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	if len(w.known) == 0 {
		t.Fatal("the known set is empty; / is always mounted")
	}
	if !w.known["/"] {
		t.Errorf("the known set does not contain /: %v", keys(w.known))
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAddFiresImmediatelyForAnAlreadyMountedPath(t *testing.T) {
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var fired bool
	if err := w.Add("/", "svc", func() { fired = true }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !fired {
		t.Error("/ is mounted but Add did not fire; a service loaded after " +
			"its mount landed would wait forever")
	}
}

func TestAddDoesNotFireForAnUnmountedPath(t *testing.T) {
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var fired bool
	if err := w.Add("/definitely-not-a-mountpoint-xyz", "svc", func() { fired = true }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if fired {
		t.Error("Add fired for a path that is not mounted")
	}
}

func TestAddDoesNotFireForAnyMountOnArm(t *testing.T) {
	// There is no "already" for the wildcard: every mount is the event,
	// so arming must not count the existing table as one.
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var fired bool
	if err := w.Add(AnyMount, "svc", func() { fired = true }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if fired {
		t.Error("the wildcard fired on arm against the existing mount table")
	}
}

func TestAddRejectsBadPaths(t *testing.T) {
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	for _, p := range []string{"", "relative/path", "mnt"} {
		if err := w.Add(p, "svc", func() {}); err == nil {
			t.Errorf("Add(%q) was accepted", p)
		}
	}
}

func TestDispatchFiresOnlyForNewlyAppearedMounts(t *testing.T) {
	// dispatch is driven off the known set, so it can be exercised
	// without mounting anything: pretend the table was smaller before.
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var mu sync.Mutex
	var hits []string
	w.regs["/"] = map[string]func(){"a": func() {
		mu.Lock()
		hits = append(hits, "root")
		mu.Unlock()
	}}
	w.regs[AnyMount] = map[string]func(){"b": func() {
		mu.Lock()
		hits = append(hits, "any")
		mu.Unlock()
	}}

	// Nothing changed since New seeded the set: no registration fires.
	w.dispatch()
	mu.Lock()
	n := len(hits)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("dispatch fired %v with an unchanged mount table", hits)
	}

	// Now forget that / was mounted; the next dispatch must treat it as
	// newly appeared, and the wildcard must fire alongside it.
	w.mu.Lock()
	delete(w.known, "/")
	w.mu.Unlock()
	w.dispatch()

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(hits, ",")
	if !strings.Contains(joined, "root") {
		t.Errorf("the path registration did not fire: %v", hits)
	}
	if !strings.Contains(joined, "any") {
		t.Errorf("the wildcard did not fire for a new mount: %v", hits)
	}
}

func TestRemoveStopsFiring(t *testing.T) {
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var fired bool
	w.regs["/"] = map[string]func(){"svc": func() { fired = true }}
	w.Remove("/", "svc")
	w.mu.Lock()
	delete(w.known, "/")
	w.mu.Unlock()
	w.dispatch()
	if fired {
		t.Error("a removed registration still fired")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Close()
	w.Close() // must not panic on a second close of the stop channel
}

func TestReadMountpointsMatchesProc(t *testing.T) {
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	got, err := w.readMountpoints()
	if err != nil {
		t.Fatalf("readMountpoints: %v", err)
	}
	// Cross-check against an independent read rather than trusting the
	// parser against itself.
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Skip("cannot read mountinfo")
	}
	want := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if len(strings.Fields(line)) >= 5 {
			want++
		}
	}
	// The map deduplicates, so it can only be smaller than the line
	// count; being wildly smaller would mean field 5 was misread.
	if len(got) == 0 || len(got)*2 < want {
		t.Errorf("parsed %d mountpoints from %d lines", len(got), want)
	}
}

// TestRunCostsNothingUntilSomethingRegisters is the test that holds the
// idle-cost promise, which is the whole reason the loop starts lazily:
// a once-per-second wakeup in PID 1 on every system that never uses
// start-on-mount would be a regression nobody asked for.
//
// It counts mount-table reads rather than wakeups, because a read is
// what a poll iteration costs and it is observable from here.
func TestRunCostsNothingUntilSomethingRegisters(t *testing.T) {
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var reads int32
	w.onRead = func() { atomic.AddInt32(&reads, 1) }

	// Comfortably longer than pollInterval: if Run polled, this would
	// have produced several reads by now.
	time.Sleep(3 * pollInterval)

	if n := atomic.LoadInt32(&reads); n != 0 {
		t.Errorf("Run performed %d mount-table reads with no registrations; "+
			"it must not poll until a service asks", n)
	}
	if w.Watching() {
		t.Error("Watching() is true with no registrations")
	}

	// Registering must start it.
	if err := w.Add("/definitely-not-a-mountpoint-xyz", "svc", func() {}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !w.Watching() {
		t.Error("Watching() is false after a registration")
	}
	time.Sleep(2 * pollInterval)
	if n := atomic.LoadInt32(&reads); n == 0 {
		t.Error("Run performed no reads after a registration; the loop never started")
	}
}

// Two services waiting for the same path (here the wildcard, which is
// what `start-on-mount = yes` registers) must both fire, and unloading
// one must leave the other armed. One registration per path made the
// last Add win and a Remove drop both.
func TestSamePathRegistrationsAreIndependent(t *testing.T) {
	w, err := New(testLogger{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var mu sync.Mutex
	hits := map[string]int{}
	reg := func(id string) func() {
		return func() { mu.Lock(); hits[id]++; mu.Unlock() }
	}
	if err := w.Add(AnyMount, "a", reg("a")); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(AnyMount, "b", reg("b")); err != nil {
		t.Fatal(err)
	}
	// The first Add wakes the loop, which re-reads the table into
	// w.known; doing that after newMount's delete below would hide the
	// "new" mount. Let it happen first. The loop's own next pass is a
	// pollInterval away, past the end of the test.
	time.Sleep(100 * time.Millisecond)
	newMount := func() {
		w.mu.Lock()
		delete(w.known, "/")
		w.mu.Unlock()
		w.dispatch()
	}

	newMount()
	mu.Lock()
	if hits["a"] != 1 || hits["b"] != 1 {
		t.Errorf("one mount: a fired %d, b fired %d; want 1 each", hits["a"], hits["b"])
	}
	mu.Unlock()

	w.Remove(AnyMount, "b")
	newMount()
	mu.Lock()
	defer mu.Unlock()
	if hits["a"] != 2 {
		t.Errorf("after removing b, a fired %d times in total, want 2", hits["a"])
	}
	if hits["b"] != 1 {
		t.Errorf("removed b fired again: %d", hits["b"])
	}
}

// A mount anyone can make in PID 1's namespace (FUSE through setuid
// fusermount, udisks for a desktop user) wakes the watcher, which
// re-reads and parses the whole mount table. With no bound on how often,
// a user looping mount/unmount had PID 1 parse the table once per
// change, thousands of times a second. systemd rate-limits its mount
// event source to 5 a second (DefaultMountRateLimitBurst/IntervalSec).
// Run as root of a private user and mount namespace, where tmpfs mounts
// stand in for FUSE ones.
func TestMountChurnIsRateLimited(t *testing.T) {
	if os.Getenv("SLINIT_TEST_MOUNTNS") == "" {
		unshare, err := exec.LookPath("unshare")
		if err != nil {
			t.Skip("unshare not available")
		}
		ns := []string{"--user", "--map-root-user", "--mount"}
		if exec.Command(unshare, append(ns, "true")...).Run() != nil {
			t.Skip("unshare --user --mount not permitted here")
		}
		cmd := exec.Command(unshare, append(ns, os.Args[0],
			"-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1")...)
		cmd.Env = append(os.Environ(), "SLINIT_TEST_MOUNTNS=1")
		out, err := cmd.CombinedOutput()
		if err != nil || strings.Contains(string(out), "--- FAIL") {
			t.Fatalf("in mount namespace: %v\n%s", err, out)
		}
		return
	}

	dir := t.TempDir()
	w, err := New(testLogger{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var reads int32
	w.onRead = func() { atomic.AddInt32(&reads, 1) }
	if err := w.Add(AnyMount, "svc", func() {}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	base := atomic.LoadInt32(&reads)
	churn := 0
	for end := time.Now().Add(time.Second); time.Now().Before(end); churn++ {
		if err := unix.Mount("tmpfs", dir, "tmpfs", 0, ""); err != nil {
			t.Fatal(err)
		}
		if err := unix.Unmount(dir, 0); err != nil {
			t.Fatal(err)
		}
	}
	n := atomic.LoadInt32(&reads) - base
	t.Logf("%d mount/unmount cycles, %d mount-table reads", churn, n)
	if n > 2*mountRateBurst {
		t.Errorf("%d mount-table reads in one second of churn, want at most %d",
			n, 2*mountRateBurst)
	}
}
