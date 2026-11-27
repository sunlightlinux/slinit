package process

import (
	"net"
	"os"
	"testing"
	"time"
)

type credHandler chan NotifyMessage

func (h credHandler) OnNotify(m NotifyMessage, fds []*os.File) { h <- m }

// Each message carries its sender's pid, from the credentials the kernel
// attaches (SO_PASSCRED): the service side decides from it who may send.
func TestNotifyMessageCarriesSenderPID(t *testing.T) {
	old := notifySocketRoot
	SetNotifySocketRoot(t.TempDir())
	defer SetNotifySocketRoot(old)
	l, err := NewNotifySocketListener("creds", uint32(os.Getuid()), uint32(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()
	h := make(credHandler, 1)
	l.Start(h)
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: l.Path(), Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("READY=1")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-h:
		if !m.Ready || m.SenderPID != os.Getpid() {
			t.Errorf("ready %v sender %d, want ready from %d", m.Ready, m.SenderPID, os.Getpid())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message")
	}
}
