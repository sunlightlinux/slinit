package main

import (
	"os"
	"testing"
	"time"
)

// addDirect registers a lock without the D-Bus layer and returns the
// client's end, which the test owns and closes to release.
func addDirect(t *testing.T, r *inhibitRegistry, what, mode string) *os.File {
	t.Helper()
	clientEnd, _, err := r.add(what, "test", "because", mode, 1000, 4242)
	if err != nil {
		t.Fatalf("add(%s, %s): %v", what, mode, err)
	}
	return clientEnd
}

// waitUntil polls cond for up to d. The registry releases locks from a
// goroutine watching the pipe, so a test cannot assert immediately.
func waitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole protocol is "the lock lasts until the fd is closed". Before
// this, Inhibit() closed the write end before returning, so the client's
// descriptor was already at EOF and no lock ever existed.
func TestInhibitorReleasedWhenClientClosesFd(t *testing.T) {
	r := newInhibitRegistry()
	client := addDirect(t, r, "sleep", "block")

	if got := len(r.list()); got != 1 {
		t.Fatalf("expected 1 lock held, got %d", got)
	}
	if r.blockedBy("sleep") == nil {
		t.Error("a block lock on sleep should block sleep")
	}
	if r.blockedBy("shutdown") != nil {
		t.Error("a lock on sleep must not block shutdown")
	}

	client.Close()
	waitUntil(t, 2*time.Second, "the lock to be released", func() bool {
		return len(r.list()) == 0
	})
	if r.blockedBy("sleep") != nil {
		t.Error("sleep still blocked after the client let go")
	}
}

// Writing into the pipe is not releasing. A client that logs through the
// wrong fd must not silently drop its own lock.
func TestInhibitorSurvivesDataOnTheFd(t *testing.T) {
	r := newInhibitRegistry()
	client := addDirect(t, r, "sleep", "delay")
	defer client.Close()

	if _, err := client.Write([]byte("not a release\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := r.delayCount("sleep"); got != 1 {
		t.Errorf("lock dropped on data rather than closure: delayCount=%d", got)
	}
}

// A colon-separated what covers each class listed, and only those.
func TestInhibitorMultiClass(t *testing.T) {
	r := newInhibitRegistry()
	client := addDirect(t, r, "sleep:handle-lid-switch", "block")
	defer client.Close()

	for _, what := range []string{"sleep", "handle-lid-switch"} {
		if r.blockedBy(what) == nil {
			t.Errorf("%q should be blocked", what)
		}
	}
	if r.blockedBy("idle") != nil {
		t.Error("idle was not in the list and must not be blocked")
	}
	if got := r.inhibitedClasses("block"); got != "handle-lid-switch:sleep" {
		t.Errorf("BlockInhibited = %q, want \"handle-lid-switch:sleep\"", got)
	}
	if got := r.inhibitedClasses("delay"); got != "" {
		t.Errorf("DelayInhibited should be empty, got %q", got)
	}
}

// A typo in a client's class list is an error rather than a lock that
// guards nothing — the failure mode that silently loses the guarantee.
func TestInhibitorRejectsUnknownClassAndMode(t *testing.T) {
	r := newInhibitRegistry()
	if _, _, err := r.add("slep", "w", "y", "block", 0, 0); err == nil {
		t.Error("a misspelled class should be rejected")
	}
	if _, _, err := r.add("sleep", "w", "y", "pause", 0, 0); err == nil {
		t.Error("an unknown mode should be rejected")
	}
	if got := len(r.list()); got != 0 {
		t.Errorf("rejected requests must not register: %d held", got)
	}
}

// waitForDelays is the handshake's pause: it returns as soon as the last
// delay holder lets go, and that is what keeps a suspend prompt-feeling
// rather than always costing the full timeout.
func TestWaitForDelaysReturnsOnRelease(t *testing.T) {
	r := newInhibitRegistry()
	client := addDirect(t, r, "sleep", "delay")

	go func() {
		time.Sleep(100 * time.Millisecond)
		client.Close()
	}()

	start := time.Now()
	if timedOut := r.waitForDelays("sleep", 3*time.Second); timedOut {
		t.Error("should have returned on release, not timed out")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waited %s; should have returned shortly after the release", elapsed)
	}
}

// And it gives up rather than waiting forever: a locker that crashed
// holding a delay lock must not keep a lid-shut laptop awake.
func TestWaitForDelaysGivesUp(t *testing.T) {
	r := newInhibitRegistry()
	client := addDirect(t, r, "sleep", "delay")
	defer client.Close()

	start := time.Now()
	if timedOut := r.waitForDelays("sleep", 200*time.Millisecond); !timedOut {
		t.Error("expected the wait to time out while the lock is held")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("returned after %s, before the cap", elapsed)
	}
}

// A block lock must not be waited out — it refuses, it does not delay.
func TestBlockLockIsNotADelay(t *testing.T) {
	r := newInhibitRegistry()
	client := addDirect(t, r, "sleep", "block")
	defer client.Close()

	if r.delayCount("sleep") != 0 {
		t.Error("a block lock must not count as a delay")
	}
	if timedOut := r.waitForDelays("sleep", 50*time.Millisecond); timedOut {
		t.Error("with no delay locks the wait should return at once")
	}
}
