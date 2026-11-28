package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakeCgroup stands in for cgroupfs: a scope is a directory holding a
// cgroup.events file, and its rmdir answers EBUSY until that file says
// it is empty, as the kernel's does. busyOnce makes the first rmdir
// answer EBUSY whatever the file says, which is the scope emptying
// between the rmdir and the watch.
type fakeCgroup struct {
	mu       sync.Mutex
	busyOnce bool
	calls    int
}

func (f *fakeCgroup) rmdir(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.busyOnce {
		f.busyOnce = false
		return unix.EBUSY
	}
	b, err := os.ReadFile(filepath.Join(path, "cgroup.events"))
	if err != nil {
		return err
	}
	// Busy unless it says it is empty: os.WriteFile truncates first,
	// and a read in between sees nothing.
	if !strings.Contains(string(b), "populated 0") {
		return unix.EBUSY
	}
	return os.RemoveAll(path)
}

func makeScope(t *testing.T, root, name string, populated bool) string {
	t.Helper()
	scope := filepath.Join(root, name)
	if err := os.Mkdir(scope, 0755); err != nil {
		t.Fatal(err)
	}
	setPopulated(t, scope, populated)
	return scope
}

func setPopulated(t *testing.T, scope string, populated bool) {
	t.Helper()
	v := "0"
	if populated {
		v = "1"
	}
	if err := os.WriteFile(filepath.Join(scope, "cgroup.events"), []byte("populated "+v+"\nfrozen 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func testReaper(t *testing.T, f *fakeCgroup) *scopeReaper {
	t.Helper()
	r := newScopeReaper(t.Logf)
	r.rmdir = f.rmdir
	t.Cleanup(r.close)
	return r
}

func waitGone(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s still there", path)
}

// TestScopeReaperRemovesOnceEmptied pins the logout leak: ReleaseSession
// comes from the session's leader, so its scope is still populated when
// the rmdir runs, and the EBUSY used to be the end of it. The scope has
// to go once the leader has left.
func TestScopeReaperRemovesOnceEmptied(t *testing.T) {
	root := t.TempDir()
	scope := makeScope(t, root, "c7", true)
	r := testReaper(t, &fakeCgroup{})

	r.remove(scope)
	if _, err := os.Stat(scope); err != nil {
		t.Fatalf("a populated scope was removed: %v", err)
	}
	if r.pending() != 1 {
		t.Fatalf("pending = %d, want the scope watched", r.pending())
	}

	setPopulated(t, scope, false) // the leader exits
	waitGone(t, scope)
	deadline := time.Now().Add(5 * time.Second)
	for r.pending() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.pending() != 0 {
		t.Fatalf("pending = %d after removal", r.pending())
	}
}

// TestScopeReaperKeepsAPopulatedScope: a scope a nohup'd job still
// lives in stays, however often cgroup.events changes, until it empties.
func TestScopeReaperKeepsAPopulatedScope(t *testing.T) {
	root := t.TempDir()
	scope := makeScope(t, root, "c8", true)
	f := &fakeCgroup{}
	r := testReaper(t, f)

	r.remove(scope)
	for i := 0; i < 3; i++ {
		setPopulated(t, scope, true)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		calls := f.calls
		f.mu.Unlock()
		if calls >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rmdir tried %d times; cgroup.events changes are not watched", calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(scope); err != nil {
		t.Fatalf("a populated scope was removed: %v", err)
	}
	if r.pending() != 1 {
		t.Fatalf("pending = %d, want 1", r.pending())
	}
	setPopulated(t, scope, false)
	waitGone(t, scope)
}

// TestScopeReaperEmptiedBeforeTheWatch: the last process can leave
// between the rmdir and the watch, and that change is never reported.
// The scope must still go.
func TestScopeReaperEmptiedBeforeTheWatch(t *testing.T) {
	root := t.TempDir()
	scope := makeScope(t, root, "c9", false)
	r := testReaper(t, &fakeCgroup{busyOnce: true})

	r.remove(scope)
	if _, err := os.Stat(scope); !os.IsNotExist(err) {
		t.Fatalf("scope still there after it emptied before the watch: %v", err)
	}
	if r.pending() != 0 {
		t.Fatalf("pending = %d, want 0", r.pending())
	}
}

// TestScopeReaperEmptyAtOnce: the usual case for a session ended by its
// leader's exit costs no watch, nor an inotify descriptor.
func TestScopeReaperEmptyAtOnce(t *testing.T) {
	root := t.TempDir()
	scope := makeScope(t, root, "c10", false)
	r := testReaper(t, &fakeCgroup{})

	r.remove(scope)
	if _, err := os.Stat(scope); !os.IsNotExist(err) {
		t.Fatalf("empty scope not removed: %v", err)
	}
	if r.file != nil {
		t.Fatal("an inotify descriptor was opened for a scope that was already empty")
	}
	r.remove(scope) // gone already: nothing to do, nothing to watch
	if r.pending() != 0 {
		t.Fatalf("pending = %d, want 0", r.pending())
	}
}

func TestOrphanScopeName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"c1", true},
		{"c42", true},
		{"c43", false}, // beyond the counter: not handed out yet
		{"c0", false},
		{"c01", false},
		{"c", false},
		{"c+1", false},
		{"c1x", false},
		{"user.slice", false},
		{"slinit", false},
		{"C1", false},
	} {
		if got := orphanScopeName(tc.name, 42); got != tc.want {
			t.Errorf("orphanScopeName(%q, 42) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSweepOrphanScopes: scopes left by an earlier slinit-logind are
// removed at startup, or watched if something still runs in them; those
// of live sessions and anything this daemon did not name are left alone.
func TestSweepOrphanScopes(t *testing.T) {
	origState, origRoot := stateRoot, sessionCgroupRoot
	stateRoot, sessionCgroupRoot = t.TempDir(), t.TempDir()
	t.Cleanup(func() { stateRoot, sessionCgroupRoot = origState, origRoot })

	if err := os.WriteFile(filepath.Join(stateRoot, "next-session-id"), []byte("5\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sessionFile("c2")), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionFile("c2"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	leaked := makeScope(t, sessionCgroupRoot, "c1", false)
	live := makeScope(t, sessionCgroupRoot, "c2", false)
	nohup := makeScope(t, sessionCgroupRoot, "c3", true)
	ahead := makeScope(t, sessionCgroupRoot, "c6", false)
	other := makeScope(t, sessionCgroupRoot, "system.slice", false)

	m := &manager{}
	m.scopes = testReaper(t, &fakeCgroup{})
	m.sweepOrphanScopes()

	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Errorf("leaked scope c1 not removed: %v", err)
	}
	for _, keep := range []string{live, nohup, ahead, other} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s removed: %v", keep, err)
		}
	}
	if m.scopes.pending() != 1 {
		t.Fatalf("pending = %d, want c3 watched", m.scopes.pending())
	}
	setPopulated(t, nohup, false)
	waitGone(t, nohup)
}

// sessionRoots points the session records and the cgroup root at
// temporary directories.
func sessionRoots(t *testing.T) {
	t.Helper()
	origState, origRoot := stateRoot, sessionCgroupRoot
	stateRoot, sessionCgroupRoot = t.TempDir(), t.TempDir()
	t.Cleanup(func() { stateRoot, sessionCgroupRoot = origState, origRoot })
	if err := os.MkdirAll(filepath.Dir(sessionFile("c1")), 0755); err != nil {
		t.Fatal(err)
	}
}

func writeRecord(t *testing.T, id, scope string) {
	t.Helper()
	b, err := json.Marshal(SessionRecord{ID: id, UserID: 4242, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionFile(id), b, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestReleaseSessionRemovesTheScopeOnceEmptied goes through
// releaseSession, the path every PAM logout takes, which once did a
// single rmdir and left the scope at EBUSY.
func TestReleaseSessionRemovesTheScopeOnceEmptied(t *testing.T) {
	sessionRoots(t)
	scope := makeScope(t, sessionCgroupRoot, "c4", true)
	writeRecord(t, "c4", scope)
	m := &manager{}
	m.scopes = testReaper(t, &fakeCgroup{})

	if err := m.releaseSession("c4"); err != nil {
		t.Fatal(err)
	}
	if m.scopes.pending() != 1 {
		t.Fatalf("pending = %d, want the leader's scope watched", m.scopes.pending())
	}
	setPopulated(t, scope, false) // the leader exits after its ReleaseSession
	waitGone(t, scope)
}

// TestKillScopeRemovesTheScopeOnceEmptied: cgroup.kill is asynchronous,
// so the scope is still populated right after it.
func TestKillScopeRemovesTheScopeOnceEmptied(t *testing.T) {
	sessionRoots(t)
	scope := makeScope(t, sessionCgroupRoot, "c5", true)
	m := &manager{}
	m.scopes = testReaper(t, &fakeCgroup{})

	killScope(scope, func(string) []int { return nil }, m.removeScope)
	if _, err := os.Stat(scope); err != nil {
		t.Fatalf("a populated scope was removed: %v", err)
	}
	setPopulated(t, scope, false)
	waitGone(t, scope)
}

// TestRemoveScopeOnlyTakesSessionScopes: the scope comes from a session
// record, and anything but <root>/<session id> is left alone.
func TestRemoveScopeOnlyTakesSessionScopes(t *testing.T) {
	sessionRoots(t)
	f := &fakeCgroup{}
	m := &manager{}
	m.scopes = testReaper(t, f)
	slice := makeScope(t, sessionCgroupRoot, "system.slice", false)
	nested := makeScope(t, slice, "c6", false)
	for _, scope := range []string{
		slice,
		nested,
		sessionCgroupRoot,
		sessionCgroupRoot + "/c6/..",
		filepath.Join(sessionCgroupRoot, "..", "c6"),
		"",
	} {
		m.removeScope(scope)
	}
	if f.calls != 0 {
		t.Fatalf("rmdir called %d times for paths that are not session scopes", f.calls)
	}
	ok := makeScope(t, sessionCgroupRoot, "c6", false)
	m.removeScope(ok + "/")
	if _, err := os.Stat(ok); !os.IsNotExist(err) {
		t.Fatalf("session scope not removed: %v", err)
	}
}

// TestScopeReaperSurvivesALostDescriptor: a reader that fails starts
// over and watches its scopes again, rather than leaving them for good.
func TestScopeReaperSurvivesALostDescriptor(t *testing.T) {
	root := t.TempDir()
	scope := makeScope(t, root, "c11", true)
	r := testReaper(t, &fakeCgroup{})
	r.remove(scope)

	r.mu.Lock()
	lost := r.file
	r.mu.Unlock()
	lost.Close() // the reader's Read fails

	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		again := r.file != nil && r.file != lost && len(r.watches) == 1
		r.mu.Unlock()
		if again {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the reaper did not watch its scope again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	setPopulated(t, scope, false)
	waitGone(t, scope)
}
