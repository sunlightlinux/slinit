package main

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
)

// sendEvent writes an event packet. A real encoder needs a live service;
// the wait reads only the handle and event bytes.
func sendEvent(conn net.Conn, kind uint8, handle uint32, event uint8) {
	payload := make([]byte, 64)
	payload[0], payload[1], payload[2], payload[3] = byte(handle), byte(handle>>8), byte(handle>>16), byte(handle>>24)
	payload[4] = event
	// The client may be done before the daemon end has finished writing;
	// the write then fails on the closed pipe, which is not a failure here.
	_ = control.WritePacket(conn, kind, payload)
}

// scripted runs fn on the daemon end of a pipe, as the daemon writes.
func scripted(t *testing.T, fn func(daemon net.Conn)) net.Conn {
	t.Helper()
	pendingEvents = nil
	t.Cleanup(func() { pendingEvents = nil })
	client, daemon := net.Pipe()
	t.Cleanup(func() { client.Close(); daemon.Close() })
	go func() { fn(daemon) }()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	return client
}

// A stop waits for Stopped, past what comes on the way there.
func TestAwaitStopWaitsForStopped(t *testing.T) {
	const h = 7
	conn := scripted(t, func(d net.Conn) {
		sendEvent(d, control.InfoServiceEvent5, h, control.SvcEventStartCancelled)
		sendEvent(d, control.InfoServiceEvent, h, control.SvcEventStartCancelled)
		sendEvent(d, control.InfoServiceEvent5, h+1, control.SvcEventStopped) // another service
		sendEvent(d, control.InfoServiceEvent, h+1, control.SvcEventStopped)
		sendEvent(d, control.InfoServiceEvent5, h, control.SvcEventStopped)
		sendEvent(d, control.InfoServiceEvent, h, control.SvcEventStopped)
	})
	if err := awaitStopOutcome(conn, h, "x"); err != nil {
		t.Errorf("awaitStopOutcome = %v", err)
	}
}

// A start issued during the stop cancels it, and that is reported.
func TestAwaitStopReportsCancel(t *testing.T) {
	conn := scripted(t, func(d net.Conn) {
		sendEvent(d, control.InfoServiceEvent5, 1, control.SvcEventStopCancelled)
		sendEvent(d, control.InfoServiceEvent, 1, control.SvcEventStopCancelled)
	})
	if err := awaitStopOutcome(conn, 1, "x"); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("awaitStopOutcome = %v, want a cancelled error", err)
	}
}

// The daemon going away mid-stop is an error, not a claimed stop.
func TestAwaitStopDaemonGone(t *testing.T) {
	conn := scripted(t, func(d net.Conn) { d.Close() })
	if err := awaitStopOutcome(conn, 1, "x"); err == nil || !strings.Contains(err.Error(), "waiting for") {
		t.Errorf("awaitStopOutcome = %v", err)
	}
}

// The v4 half of a transition can still be unread when v5 arrived before
// the command's reply: the wait must read it, not return on v5, or restart
// would take it for the outcome of the start that follows.
func TestAwaitStopConsumesTheWholePair(t *testing.T) {
	conn := scripted(t, func(d net.Conn) {
		sendEvent(d, control.InfoServiceEvent, 1, control.SvcEventStopped)
		sendEvent(d, control.InfoServiceEvent5, 1, control.SvcEventStarted)
		sendEvent(d, control.InfoServiceEvent, 1, control.SvcEventStarted)
	})
	pendingEvents = []pendingEvent{{handle: 1, event: control.SvcEventStopped}} // v5 stashed
	if err := awaitStopOutcome(conn, 1, "x"); err != nil {
		t.Fatalf("awaitStopOutcome = %v", err)
	}
	pendingEvents = nil
	if err := awaitStartOutcome(conn, 1, "x"); err != nil {
		t.Errorf("the start after the stop saw a stale event: %v", err)
	}
}

// Starting a stopping service begins with StopCancelled, which is not how
// the start ends.
func TestAwaitStartIgnoresStopCancelled(t *testing.T) {
	conn := scripted(t, func(d net.Conn) {
		sendEvent(d, control.InfoServiceEvent, 1, control.SvcEventStopCancelled)
		sendEvent(d, control.InfoServiceEvent, 1, control.SvcEventFailedStart)
	})
	if err := awaitStartOutcome(conn, 1, "x"); err == nil || !strings.Contains(err.Error(), "failed to start") {
		t.Errorf("awaitStartOutcome = %v, want failed to start", err)
	}
}
