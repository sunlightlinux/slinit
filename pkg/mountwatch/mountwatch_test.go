package mountwatch

import (
	"os"
	"strings"
	"sync"
	"testing"
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
	if err := w.Add("/", func() { fired = true }); err != nil {
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
	if err := w.Add("/definitely-not-a-mountpoint-xyz", func() { fired = true }); err != nil {
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
	if err := w.Add(AnyMount, func() { fired = true }); err != nil {
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
		if err := w.Add(p, func() {}); err == nil {
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
	w.regs["/"] = &registration{path: "/", fn: func() {
		mu.Lock()
		hits = append(hits, "root")
		mu.Unlock()
	}}
	w.regs[AnyMount] = &registration{path: AnyMount, fn: func() {
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
	w.regs["/"] = &registration{path: "/", fn: func() { fired = true }}
	w.Remove("/")
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
