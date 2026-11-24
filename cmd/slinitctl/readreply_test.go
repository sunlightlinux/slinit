package main

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/control"
)

// A handle this connection already holds pushes its state changes
// unasked, so one can land ahead of the reply to the next command.
// `run --wait` / `run --collect` re-load the transient unit while it
// changes state; reading that event as the reply failed them with
// "unexpected reply: 101".
func TestLoadServiceHandleSkipsPushedEvent(t *testing.T) {
	conn := scripted(t, func(d net.Conn) {
		if _, _, err := control.ReadPacket(d); err != nil {
			return
		}
		sendEvent(d, control.InfoServiceEvent5, 3, control.SvcEventStopped)
		sendEvent(d, control.InfoServiceEvent, 3, control.SvcEventStopped)
		rec := make([]byte, 6)
		binary.LittleEndian.PutUint32(rec[1:5], 9)
		_ = control.WritePacket(d, control.RplyServiceRecord, rec)
	})
	h, err := loadServiceHandle(conn, "x")
	if err != nil || h != 9 {
		t.Fatalf("loadServiceHandle = %d, %v; want 9, nil", h, err)
	}
	// The event is kept for whoever waits on handle 3, not dropped.
	if done, _ := takeStopOutcome(3, "y"); !done {
		t.Errorf("pushed Stopped for handle 3 was dropped: %+v", pendingEvents)
	}
}

// Every other reply read shares the hazard; one sample of the
// multi-packet listings.
func TestListSkipsPushedEvent(t *testing.T) {
	conn := scripted(t, func(d net.Conn) {
		if _, _, err := control.ReadPacket(d); err != nil {
			return
		}
		sendEvent(d, control.InfoServiceEvent5, 3, control.SvcEventStarted)
		_ = control.WritePacket(d, control.RplyListDone, nil)
	})
	if err := cmdList(conn); err != nil {
		t.Fatalf("cmdList = %v", err)
	}
}
