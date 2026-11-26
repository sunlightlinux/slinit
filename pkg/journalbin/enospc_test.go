package journalbin

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// TestWriterRecoversAfterFullDisk: an append that fails part way (the
// disk filled at some byte of it) must leave the writer able to go on
// once space is back, and the file readable with every entry whose
// append succeeded.
func TestWriterRecoversAfterFullDisk(t *testing.T) {
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	key, err := NewFSSKey(0, DefaultFSSEpochUsec)
	if err != nil {
		t.Fatal(err)
	}
	for _, sealed := range []bool{false, true} {
		for _, k := range []int{1, 4} {
			// Size of the file before and after the append that fails.
			probe := filepath.Join(dir, "probe")
			_ = os.Remove(probe)
			opts := WriterOptions{}
			if sealed {
				opts.FSSKey, opts.TagEvery = key, 2
			}
			w, err := NewWriterWithOptions(probe, opts)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < k; i++ {
				w.Append(tornEvent(i, fmt.Sprintf("m%d", i)))
			}
			st, _ := os.Stat(probe)
			lo := st.Size()
			w.Append(tornEvent(k, "the one that hits the full disk"))
			st, _ = os.Stat(probe)
			hi := st.Size()
			w.Close()

			for limit := lo; limit < hi; limit++ {
				name := fmt.Sprintf("sealed=%v k=%d limit=%d", sealed, k, limit)
				path := filepath.Join(dir, "j.journal")
				_ = os.Remove(path)
				w, err := NewWriterWithOptions(path, opts)
				if err != nil {
					t.Fatal(err)
				}
				var want []string
				for i := 0; i < k; i++ {
					if _, err := w.Append(tornEvent(i, fmt.Sprintf("m%d", i))); err != nil {
						t.Fatal(err)
					}
					want = append(want, fmt.Sprintf("m%d", i))
				}
				if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: uint64(limit), Max: old.Max}); err != nil {
					t.Skipf("cannot lower RLIMIT_FSIZE: %v", err)
				}
				_, aerr := w.Append(tornEvent(k, "the one that hits the full disk"))
				if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
					t.Fatal(err)
				}
				maybe := len(want)
				if aerr == nil {
					want = append(want, "the one that hits the full disk")
					maybe = -1
				}
				for i := k + 1; i < k+4; i++ {
					msg := fmt.Sprintf("after %d", i)
					if _, err := w.Append(tornEvent(i, msg)); err != nil {
						t.Errorf("%s: append after space came back: %v", name, err)
						break
					}
					want = append(want, msg)
				}
				if err := w.Close(); err != nil {
					t.Errorf("%s: close: %v", name, err)
				}

				if err := readsBack(path, want, maybe, opts.FSSKey); err != nil {
					t.Errorf("%s (append err %v): %v", name, aerr, err)
				}
			}
		}
	}
}

// readsBack checks the file reads back want, through the object walk and
// the entry arrays alike. When maybe >= 0, the entry whose append failed
// may also be found at that position.
func readsBack(path string, want []string, maybe int, key *FSSKey) error {
	r, err := OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	var got []string
	if err := r.Iter(func(e *journal.Event) bool { got = append(got, e.Msg); return true }); err != nil {
		return fmt.Errorf("Iter after %v: %w", got, err)
	}
	if maybe >= 0 && len(got) == len(want)+1 && got[maybe] == "the one that hits the full disk" {
		want = append(want[:maybe:maybe], got[maybe:]...)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("Iter read %v, want %v", got, want)
	}
	offs, err := r.EntryOffsets()
	if err != nil {
		return fmt.Errorf("EntryOffsets: %w", err)
	}
	got = nil
	for _, o := range offs {
		e, err := r.ReadEntryAt(o)
		if err != nil {
			return fmt.Errorf("ReadEntryAt(%d): %w", o, err)
		}
		got = append(got, e.Msg)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("array read %v, want %v", got, want)
	}
	if key != nil {
		res, err := Verify(path, key)
		if err != nil || !res.OK() {
			return fmt.Errorf("verify: %+v, %v", res, err)
		}
	}
	return nil
}
