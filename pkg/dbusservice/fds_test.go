package dbusservice

import (
	"os"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/internal/dbustest"
)

const (
	fdPath  = dbus.ObjectPath("/test/fds")
	fdIface = "test.FDs"
)

// fdMethods is a service with one method that takes a descriptor and one
// that does not.
type fdMethods struct{}

// Take reports whether fd was open, and a pipe, while the method ran.
func (fdMethods) Take(fd dbus.UnixFD) (bool, *dbus.Error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(fd), &st); err != nil {
		return false, nil
	}
	return st.Mode&unix.S_IFMT == unix.S_IFIFO, nil
}

func (fdMethods) Plain(s []string) *dbus.Error { return nil }

func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	return len(ents)
}

// Every descriptor a caller sends is closed once its call is answered,
// whether the method took it, the method takes none, the method does not
// exist, or no answer was wanted; and a method that takes one can use it
// while it runs. They were all kept open, so any local user could fill
// a daemon's descriptor table and get its bus connection dropped.
func TestReceivedFDsAreClosed(t *testing.T) {
	server, client := dbustest.Pair(t, CloseReceivedFDs()...)
	if err := server.Export(fdMethods{}, fdPath, fdIface); err != nil {
		t.Fatal(err)
	}
	obj := client.Object("test.server", fdPath)

	// One round first, so whatever godbus allocates once is not counted.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	pipe := dbus.UnixFD(r.Fd())
	var ok bool
	if err := obj.Call(fdIface+".Take", 0, pipe).Store(&ok); err != nil || !ok {
		t.Fatalf("Take: ok=%v err=%v; the method must see an open descriptor", ok, err)
	}

	before := openFDs(t)
	const rounds = 50
	for i := 0; i < rounds; i++ {
		if err := obj.Call(fdIface+".Take", 0, pipe).Store(&ok); err != nil || !ok {
			t.Fatalf("Take: ok=%v err=%v", ok, err)
		}
		// The wrong argument type: refused before any method runs.
		if err := obj.Call(fdIface+".Plain", 0, pipe).Err; err == nil {
			t.Fatal("Plain with a descriptor was not refused")
		}
		if err := obj.Call(fdIface+".Missing", 0, pipe).Err; err == nil {
			t.Fatal("a method that does not exist answered")
		}
		obj.Call(fdIface+".Take", dbus.FlagNoReplyExpected, pipe)
		// Descriptors inside a variant and a struct, one named twice.
		inner := struct {
			S  string
			FD dbus.UnixFD
		}{"x", pipe}
		if err := obj.Call(fdIface+".Plain", 0, dbus.MakeVariant(pipe), inner).Err; err == nil {
			t.Fatal("Plain with a variant was not refused")
		}
	}
	// A call after the no-reply one: its answer means the one before it
	// has been read and dealt with.
	if err := obj.Call(fdIface+".Plain", 0, []string{"sync"}).Err; err != nil {
		t.Fatal(err)
	}
	if after := openFDs(t); after > before {
		t.Errorf("%d descriptors left open after %d rounds of calls", after-before, rounds)
	}
}

// A call that wants no reply never sees its descriptors: nothing would
// say when the method is done with them.
func TestNoReplyCallGetsNoFDs(t *testing.T) {
	c := &fdCloser{pending: map[callKey]*pendingCall{}}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	fd, err := unix.Dup(int(r.Fd()))
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	msg := &dbus.Message{Type: dbus.TypeMethodCall, Flags: dbus.FlagNoReplyExpected,
		Headers: map[dbus.HeaderField]dbus.Variant{}, Body: []any{dbus.UnixFD(fd)}}
	c.incoming(msg)
	if msg.Body != nil {
		t.Error("the call kept its arguments")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != unix.EBADF {
		t.Errorf("descriptor still open: %v", err)
	}
	if len(c.pending) != 0 {
		t.Errorf("pending = %v", c.pending)
	}
}

// Losing the bus connection ends WaitIdle with an error, so the daemon
// exits and is started again. It used to wait forever with no bus.
func TestWaitIdleReturnsWhenConnectionLost(t *testing.T) {
	for _, idle := range []time.Duration{0, time.Hour} {
		server, _ := dbustest.Pair(t)
		s := &Service{Conn: server}
		done := make(chan error, 1)
		go func() { done <- s.WaitIdle(idle) }()
		server.Close()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("idle %v: WaitIdle returned nil for a lost connection", idle)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("idle %v: WaitIdle still waiting with the connection gone", idle)
		}
	}
}

// A peer picks its own serials. A second call reusing the key of one
// still in flight used to have its answer close the first call's
// descriptors while that call's method was still using them (a slow
// polkit check before CreateSessionWithPIDFD reads its pidfd).
func TestDuplicateSerialKeepsFirstCallsFDs(t *testing.T) {
	c := &fdCloser{pending: map[callKey]*pendingCall{}}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	fd, err := unix.Dup(int(r.Fd()))
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	hdr := func() map[dbus.HeaderField]dbus.Variant {
		return map[dbus.HeaderField]dbus.Variant{dbus.FieldSender: dbus.MakeVariant(":1.5")}
	}
	first := &dbus.Message{Type: dbus.TypeMethodCall, Headers: hdr(), Body: []any{dbus.UnixFD(fd)}}
	second := &dbus.Message{Type: dbus.TypeMethodCall, Headers: hdr(), Body: []any{"x"}}
	c.incoming(first)
	c.incoming(second) // same sender, same serial
	answer := func() {
		c.outgoing(&dbus.Message{Type: dbus.TypeMethodReply, Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldReplySerial: dbus.MakeVariant(uint32(0)),
			dbus.FieldDestination: dbus.MakeVariant(":1.5"),
		}})
	}
	open := func() bool {
		_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		return err != unix.EBADF
	}
	answer() // the second call's answer
	if !open() {
		t.Fatal("the first call's descriptor was closed by another call's answer")
	}
	answer() // the first call's answer
	if open() {
		unix.Close(fd)
		t.Error("descriptor still open after every call under the key was answered")
	}
}
