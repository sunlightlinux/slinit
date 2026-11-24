package main

import (
	"io"
	"log"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/autofs"
)

// TestExpirySweepOffLoop: AUTOFS_IOC_EXPIRE_MULTI does not return
// until the daemon has answered the EXPIRE packet it queues, and the
// event loop is what reads that packet. A sweep run on the loop
// waits on itself for good. It must run elsewhere, one at a time.
func TestExpirySweepOffLoop(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	d := &daemon{
		cfg:          &daemonConfig{},
		logger:       log.New(io.Discard, "", 0),
		activeMounts: []*autofs.AutofsMount{{}},
		expire: func(*autofs.AutofsMount) (int, error) {
			calls.Add(1)
			<-release // the kernel waiting for the loop to answer
			return 1, nil
		},
	}
	returned := make(chan struct{})
	go func() {
		d.startExpirySweep()
		d.startExpirySweep() // a tick while the first sweep still runs
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("the expiry sweep blocked the event loop waiting for an EXPIRE answer only the loop can give")
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("%d sweeps running at once, want 1", n)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for d.sweeping.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	d.startExpirySweep()
	deadline = time.Now().Add(2 * time.Second)
	for calls.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("no new sweep after the first finished (calls=%d)", n)
	}
}
