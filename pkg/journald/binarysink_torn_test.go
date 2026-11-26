package journald

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

func tornSinkEvent(i int) *journal.Event {
	return &journal.Event{Ts: int64(i+1) * 1e9, Mts: int64(i+1) * 1e6, Unit: "x.service",
		Msg: fmt.Sprintf("event %d", i)}
}

// A slinit-journald killed part way through an append leaves today's
// file with the append's objects, hash-bucket links and array slot on
// disk up to wherever it died, and a header that does not take them in.
// Started again, the sink appended on top of that: the old slot then
// named whatever the next append wrote there, and the directory no
// longer read. The file is now rotated away as it is and a new one
// started, so both read: every entry the old header describes, then
// every new one.
func TestBinarySinkRotatesTornDayFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.journal")
	for _, k := range []int{1, 4} {
		_ = os.Remove(src)
		w, err := journalbin.NewWriter(src, "", "")
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < k; i++ {
			if _, err := w.Append(tornSinkEvent(i)); err != nil {
				t.Fatal(err)
			}
		}
		before, _ := os.ReadFile(src)
		if _, err := w.Append(tornSinkEvent(k)); err != nil {
			t.Fatal(err)
		}
		full, _ := os.ReadFile(src)
		w.Close()

		for cut := len(before); cut <= len(full); cut++ {
			torn := append([]byte(nil), full[:cut]...)
			copy(torn, before[:journalbin.HeaderSize])
			dir := t.TempDir()
			path := filepath.Join(dir, binaryCurrentFileName(time.Now().UTC()))
			if err := os.WriteFile(path, torn, 0o640); err != nil {
				t.Fatal(err)
			}
			s, err := NewBinarySink(BinarySinkOptions{Dir: dir, FsyncEvery: 1})
			if err != nil {
				t.Fatalf("k=%d cut=%d: start: %v", k, cut, err)
			}
			for i := k + 1; i < k+4; i++ {
				if err := s.Handle(tornSinkEvent(i)); err != nil {
					t.Fatalf("k=%d cut=%d: handle: %v", k, cut, err)
				}
			}
			s.Close()

			mr, err := journalbin.OpenDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			err = mr.Iter(func(e *journal.Event) bool { got = append(got, e.Msg); return true })
			mr.Close()
			var want []string
			for i := 0; i < k; i++ {
				want = append(want, fmt.Sprintf("event %d", i))
			}
			for i := k + 1; i < k+4; i++ {
				want = append(want, fmt.Sprintf("event %d", i))
			}
			if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("k=%d cut=%d: read %v, %v; want %v", k, cut, got, err, want)
			}
		}
	}
}
