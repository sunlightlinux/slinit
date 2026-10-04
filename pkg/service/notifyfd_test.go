package service

import (
	"errors"
	"os"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// A notify datagram can carry SCM_RIGHTS descriptors regardless of what
// the message says. Only FDSTORE=1 means "keep these"; anything else has
// to close them, or PID 1 accumulates descriptors it will never use and
// only the GC finalizer ever releases them.
//
// The shape is CVE-2021-33910's — unbounded resource use in PID 1 from
// untrusted input — reached from a service rather than a mount path.
func TestOnNotifyClosesDescriptorsItDoesNotStore(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "notify-svc")
	set.AddService(svc)
	rec := svc.Record()
	rec.SetFDStoreMax(4) // the socket only exists when the store is enabled

	for _, tc := range []struct {
		name string
		msg  process.NotifyMessage
	}{
		{"status line with descriptors attached", process.NotifyMessage{Status: "working"}},
		{"empty message with descriptors attached", process.NotifyMessage{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()

			rec.OnNotify(tc.msg, []*os.File{r})

			// A second Close reports ErrClosed only if the first one
			// happened. Reading would also fail, but this says which.
			if err := r.Close(); !errors.Is(err, os.ErrClosed) {
				t.Errorf("descriptor was not closed: second Close returned %v, "+
					"want os.ErrClosed — the fd leaked into PID 1", err)
			}
		})
	}
}

// The storing path must NOT close them: the store owns them from there on
// and replays them into the next start.
func TestOnNotifyKeepsStoredDescriptorsOpen(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "notify-store-svc")
	set.AddService(svc)
	rec := svc.Record()
	rec.SetFDStoreMax(4)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	rec.OnNotify(process.NotifyMessage{FDStore: true, FDName: "listener"}, []*os.File{r})

	if err := r.Close(); errors.Is(err, os.ErrClosed) {
		t.Error("a stored descriptor was closed — the store replays these " +
			"into the next start, so closing them defeats the feature")
	}
}
