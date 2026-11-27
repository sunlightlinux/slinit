package main

import (
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// sameFile reports whether fd is still open on the file st describes.
// Comparing the inode, not only that the number is open, keeps a number
// the kernel has handed to something else from passing.
func sameFile(fd int, st *unix.Stat_t) bool {
	var got unix.Stat_t
	if err := unix.Fstat(fd, &got); err != nil {
		return false
	}
	return got.Dev == st.Dev && got.Ino == st.Ino && got.Rdev == st.Rdev
}

// godbus sends a reply's descriptors after the method has returned, by
// number, and leaves them open. TakeDevice returned the number of our own
// copy of the device, which a ReleaseDevice or a controller leaving the
// bus can close before the reply goes out — and the kernel can then give
// the number to another file, which the compositor would be handed. The
// descriptor in the reply must stay open until the reply is sent.
func TestTakeDeviceReplyFDOutlivesRelease(t *testing.T) {
	resetDevices()
	defer resetDevices()
	fakeSysDev(t, 13, 64, "input", "event0", "null")
	var null unix.Stat_t
	if err := unix.Stat("/dev/null", &null); err != nil {
		t.Skipf("no /dev/null: %v", err)
	}

	fd, _, err := takeDevice("c1", 13, 64)
	if err != nil {
		t.Fatal(err)
	}
	releaseDevice("c1", 13, 64)
	if !sameFile(int(fd), &null) {
		t.Errorf("descriptor %d for TakeDevice's reply was closed by ReleaseDevice before the reply could be sent", fd)
	}
}

// Inhibit handed godbus the number of an *os.File a timer closes, read
// while that timer could run: `go test -race` reported it. The reply's
// descriptor is now one of its own, closed after fdSendGrace, and the
// lock lasts for as long as the client holds its copy.
func TestInhibitReplyFDIsItsOwn(t *testing.T) {
	m := &manager{inhibitors: newInhibitRegistry()}
	fakeBus(t, map[dbus.Sender]uint32{":1.u": 1000})
	prev := fdSendGrace
	fdSendGrace = 50 * time.Millisecond
	t.Cleanup(func() { fdSendGrace = prev })

	fd, derr := m.Inhibit(":1.u", "sleep", "w", "y", "delay")
	if derr != nil {
		t.Fatal(derr)
	}
	// What godbus does with the reply: send the number, here by dup.
	client, err := unix.Dup(int(fd))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(fdSendGrace + 100*time.Millisecond)
	if got := len(m.inhibitors.list()); got != 1 {
		t.Fatalf("%d locks held while the client holds its descriptor, want 1", got)
	}
	unix.Close(client)
	// Generous: the release is seen by a goroutine reading the pipe
	// through the poller, which a loaded test host can schedule late.
	waitUntil(t, 10*time.Second, "the lock to be released", func() bool {
		return len(m.inhibitors.list()) == 0
	})
}
