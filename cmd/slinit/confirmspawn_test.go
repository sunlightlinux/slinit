//go:build linux

package main

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// The confirm-spawn prompt runs in BringUp with the scheduling lock
// held. It used to read the console with no limit, so a boot with
// slinit.confirm-spawn and nobody at the console stopped there for
// ever, the whole state machine and control socket with it.
func TestConfirmSpawnGivesUpAndProceeds(t *testing.T) {
	con := newPtyConsole(t)
	ask := makeConfirmSpawnPrompt(logging.New(logging.LevelError), con.path, 200*time.Millisecond)

	answer := make(chan bool, 1)
	go func() { answer <- ask("svc") }()
	select {
	case yes := <-answer:
		if !yes {
			t.Error("an unanswered prompt skipped the service; the default is yes")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the prompt waited past its timeout for an answer nobody gave")
	}
	con.waitFor(t, 0, "defaulting to yes")
}

// An answer within the time still counts.
func TestConfirmSpawnHonoursAnAnswer(t *testing.T) {
	con := newPtyConsole(t)
	ask := makeConfirmSpawnPrompt(logging.New(logging.LevelError), con.path, 5*time.Second)

	answer := make(chan bool, 1)
	go func() { answer <- ask("svc") }()
	con.waitFor(t, 0, "[Y/n]")
	con.key(t, 'n')
	select {
	case yes := <-answer:
		if yes {
			t.Error("'n' did not skip the service")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the prompt did not take the answer")
	}
}
