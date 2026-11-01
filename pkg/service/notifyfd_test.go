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

// FDSTOREWIPE= and FDSTOREREMOVE=1, in systemd's order of precedence.
func TestOnNotifyFDStoreRemoval(t *testing.T) {
	setup := func(t *testing.T, names ...string) (*ServiceRecord, map[string]*os.File) {
		t.Helper()
		set, _ := newTestSet()
		svc := NewProcessService(set, "notify-rm-svc")
		set.AddService(svc)
		rec := svc.Record()
		rec.SetFDStoreMax(8)
		files := map[string]*os.File{}
		for _, n := range names {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { w.Close() })
			files[n] = r
			rec.OnNotify(process.NotifyMessage{FDStore: true, FDName: n}, []*os.File{r})
		}
		return rec, files
	}
	closed := func(f *os.File) bool { return errors.Is(f.Close(), os.ErrClosed) }
	pipe := func(t *testing.T) *os.File {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { w.Close() })
		return r
	}

	t.Run("wipe by glob", func(t *testing.T) {
		rec, files := setup(t, "conn-1", "conn-2", "listener")
		rec.OnNotify(process.NotifyMessage{HasFDStoreWipe: true, FDStoreWipe: "conn-*"}, nil)
		if got := rec.FDStore().Len(); got != 1 {
			t.Fatalf("store has %d after FDSTOREWIPE=conn-*, want 1", got)
		}
		if !closed(files["conn-1"]) || closed(files["listener"]) {
			t.Error("wipe closed the wrong descriptors")
		}
	})

	t.Run("wipe takes precedence over FDSTORE=1", func(t *testing.T) {
		rec, _ := setup(t, "a")
		extra := pipe(t)
		rec.OnNotify(process.NotifyMessage{HasFDStoreWipe: true, FDStoreWipe: "*", FDStore: true, FDName: "b"},
			[]*os.File{extra})
		if got := rec.FDStore().Len(); got != 0 {
			t.Errorf("store has %d, want 0: the attached fd must not be stored", got)
		}
		if !closed(extra) {
			t.Error("the descriptor attached to a wipe was not closed")
		}
	})

	t.Run("invalid wipe pattern is ignored", func(t *testing.T) {
		rec, _ := setup(t, "a")
		for _, p := range []string{"", "has:colon"} {
			rec.OnNotify(process.NotifyMessage{HasFDStoreWipe: true, FDStoreWipe: p}, nil)
		}
		if got := rec.FDStore().Len(); got != 1 {
			t.Errorf("store has %d after invalid wipes, want 1", got)
		}
	})

	t.Run("remove by exact name", func(t *testing.T) {
		rec, files := setup(t, "conn-1", "conn-*")
		// FDSTOREREMOVE matches literally: "conn-*" is a name here.
		rec.OnNotify(process.NotifyMessage{FDStoreRemove: true, FDName: "conn-*"}, nil)
		if got := rec.FDStore().Len(); got != 1 || !closed(files["conn-*"]) || closed(files["conn-1"]) {
			t.Errorf("FDSTOREREMOVE=1 FDNAME=conn-*: store %d, want only conn-1 left", got)
		}
	})

	t.Run("remove without a name is ignored", func(t *testing.T) {
		rec, _ := setup(t, "stored")
		extra := pipe(t)
		rec.OnNotify(process.NotifyMessage{FDStoreRemove: true, FDStore: true}, []*os.File{extra})
		if got := rec.FDStore().Len(); got != 1 {
			t.Errorf("store has %d, want 1: no FDNAME= means nothing to remove, and nothing added", got)
		}
		if !closed(extra) {
			t.Error("the descriptor attached to a removal was not closed")
		}
	})
}
