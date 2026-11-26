package journalbin

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

func tornEvent(i int, msg string) *journal.Event {
	return &journal.Event{Ts: int64(i+1) * 1e9, Mts: int64(i+1) * 1e6, Msg: msg, Unit: "svc"}
}

// tornFiles calls fn with every file a writer leaves when it dies part
// way through appending entry k+1: its objects and in-place updates
// (hash buckets, array slots and links) reached the file up to some
// byte, and the header still describes k entries.
func tornFiles(t *testing.T, fn func(name string, torn []byte, opts WriterOptions, k int)) {
	t.Helper()
	dir := t.TempDir()
	for _, sealed := range []bool{false, true} {
		// 4 entries fill the first ENTRY_ARRAY, so the fifth links a
		// second one.
		for _, k := range []int{1, 4, 5} {
			src := filepath.Join(dir, fmt.Sprintf("src-%v-%d", sealed, k))
			opts := WriterOptions{BootID: "0123456789abcdef0123456789abcdef"}
			if sealed {
				key, err := NewFSSKey(0, DefaultFSSEpochUsec)
				if err != nil {
					t.Fatal(err)
				}
				opts.FSSKey, opts.TagEvery = key, 100
			}
			w, err := NewWriterWithOptions(src, opts)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < k; i++ {
				if _, err := w.Append(tornEvent(i, fmt.Sprintf("m%d", i))); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Append(tornEvent(k, "the torn one, a new payload")); err != nil {
				t.Fatal(err)
			}
			full, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			for cut := len(before); cut <= len(full); cut++ {
				torn := append([]byte(nil), full[:cut]...)
				copy(torn, before[:HeaderSize])
				fn(fmt.Sprintf("sealed=%v k=%d cut=%d", sealed, k, cut), torn, opts, k)
			}
		}
	}
}

// TestReaderTornTail: the entries a torn file's header describes read
// back through the ENTRY_ARRAY chain, as slinit-journalctl -D and the
// sd_journal API read them. An array slot or link the dead writer made
// for its unfinished entry lies past the header's tail and used to be
// followed: the whole directory then failed to read.
func TestReaderTornTail(t *testing.T) {
	tornFiles(t, func(name string, torn []byte, _ WriterOptions, k int) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "t.journal"), torn, 0o640); err != nil {
			t.Fatal(err)
		}
		mr, err := OpenDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		defer mr.Close()
		var got []string
		if err := mr.Iter(func(e *journal.Event) bool { got = append(got, e.Msg); return true }); err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		if len(got) != k || got[k-1] != fmt.Sprintf("m%d", k-1) {
			t.Errorf("%s: read %v, want the %d entries the header describes", name, got, k)
		}

		// slinit-journalctl --file walks the objects instead.
		r, err := OpenReader(filepath.Join(dir, "t.journal"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		defer r.Close()
		got = nil
		if err := r.Iter(func(e *journal.Event) bool { got = append(got, e.Msg); return true }); err != nil {
			t.Errorf("%s: Iter: %v", name, err)
			return
		}
		if len(got) != k {
			t.Errorf("%s: Iter read %v, want the %d entries the header describes", name, got, k)
		}
	})
}
