package main

import (
	"net"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/control"
)

// With no event in the ring buffer and no journal on disk, --list-boots
// prints the current boot alone. It asked journal.BootID() for it, and
// this process never initialises the journal's ID cache (InitIDs is
// PID 1's), so the getter panicked: "BootID() called before InitIDs".
func TestListBootsWithNoEventsDoesNotPanic(t *testing.T) {
	old := listBootsJournalDirs
	listBootsJournalDirs = []string{t.TempDir()}
	t.Cleanup(func() { listBootsJournalDirs = old })

	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		if _, _, err := control.ReadPacket(server); err != nil {
			return
		}
		control.WritePacket(server, control.RplyJournalDone, nil)
	}()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("--list-boots with no events panicked: %v", r)
		}
	}()
	if err := runListBoots(client); err != nil {
		t.Fatalf("runListBoots: %v", err)
	}
}
