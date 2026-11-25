//go:build linux && cgo

package utmp

import (
	"testing"
	"time"
)

// glibc keeps one utmpx cursor per process, and a walk spans many cgo
// calls: a write from another goroutine between two of them rewinds or
// closes the file under the walk. That interleaving can't be forced
// from a test, so this checks the structure that prevents it: every
// function using the cursor waits for utmpMu, which a walk holds for
// its whole run.
func TestUtmpCursorUsersHoldTheLock(t *testing.T) {
	users := map[string]func(){
		"ListUserTTYs":     func() { ListUserTTYs() },
		"ListUserSessions": func() { ListUserSessions() },
		"CreateEntryMode":  func() { CreateEntryMode("tst", "pts/99", 99999, "") },
		"ClearEntry":       func() { ClearEntry("tst", "pts/99") },
	}
	// LogBoot and LogoutAllUsers are left out: run as root they would
	// truncate the real utmp or log out every real session.
	for name, f := range users {
		utmpMu.Lock()
		done := make(chan struct{})
		go func() { f(); close(done) }()
		select {
		case <-done:
			t.Errorf("%s ran while utmpMu was held", name)
		case <-time.After(50 * time.Millisecond):
		}
		utmpMu.Unlock()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatalf("%s did not finish after utmpMu was released", name)
		}
	}
}
