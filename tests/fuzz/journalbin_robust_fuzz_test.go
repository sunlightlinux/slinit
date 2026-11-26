package fuzz

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
	"github.com/sunlightlinux/slinit/pkg/journalbin/sd"
)

// journalFuzzDeadline bounds one input. A corrupted chain that loops,
// or a count that makes a walk effectively endless, is a hang to the
// operator running slinit-journalctl; native Go fuzzing has no
// per-input timeout, so the target keeps its own.
const journalFuzzDeadline = 10 * time.Second

// journalFuzzAllocBudget is how much an input may allocate in all,
// over what the whole sequence of reader calls below may honestly need
// for a file of that size. A size or count field that makes a reader
// allocate far past the file is the unbounded allocation this checks.
func journalFuzzAllocBudget(fileLen int) uint64 {
	return 256*uint64(fileLen) + 64<<20
}

// runBounded runs fn on its own goroutine and fails the input when it
// takes longer than journalFuzzDeadline or allocates more than budget
// bytes. A panic in fn still fails the input: it is re-raised here.
func runBounded(t *testing.T, budget uint64, fn func()) {
	t.Helper()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	done := make(chan any, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Sprintf("%v\n%s", p, debug.Stack())
				return
			}
			done <- nil
		}()
		fn()
	}()
	select {
	case p := <-done:
		if p != nil {
			panic(p)
		}
	case <-time.After(journalFuzzDeadline):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("no result after %v (hang):\n%s", journalFuzzDeadline, buf[:n])
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > budget {
		t.Fatalf("allocated %d bytes, budget %d", got, budget)
	}
}

// FuzzJournalBinaryReadAll opens arbitrary bytes as a binary journal
// and drives every read path slinit-journalctl reaches on --file,
// --directory and --verify: the linear walk, the ENTRY_ARRAY chain
// forwards and backwards, realtime seeks, the sd_journal API with
// cursors, the multi-file merge, and FSS verification. Each must
// return an error or a result: never panic, hang or allocate without
// bound, since the file may be a crashed writer's or someone else's.
func FuzzJournalBinaryReadAll(f *testing.F) {
	for _, s := range journalSeeds(f) {
		f.Add(s)
	}
	key := fuzzFSSKey(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "system.journal")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Skip()
		}
		runBounded(t, journalFuzzAllocBudget(len(data)), func() {
			r, err := journalbin.OpenReader(path)
			if err != nil {
				return
			}
			defer r.Close()
			_ = r.Iter(func(*journal.Event) bool { return true })
			offs, err := r.EntryOffsets()
			if err == nil {
				for _, off := range offs {
					_, _ = r.ReadEntryAt(off)
				}
				for i := len(offs) - 1; i >= 0; i-- {
					_, _ = r.ReadEntryAt(offs[i])
				}
			}
			for _, target := range []uint64{0, 1, 1 << 40, ^uint64(0)} {
				_, _, _ = r.SeekRealtime(target)
			}

			if j, err := sd.OpenFiles(path); err == nil {
				var cursors []string
				for j.Next() {
					_, _ = j.GetData("MESSAGE")
					_, _ = j.GetMonotonicUsec()
					if c, err := j.GetCursor(); err == nil {
						cursors = append(cursors, c)
					}
				}
				for j.Previous() {
					_, _ = j.Current()
				}
				for _, c := range cursors {
					if j.SeekCursor(c) == nil && j.Next() {
						_ = j.TestCursor(c)
					}
				}
				_ = j.SeekRealtimeUsec(0)
				_ = j.SeekTail()
				_ = j.AddMatch("PRIORITY=6")
				for j.Previous() {
				}
				_ = j.Close()
			}

			if mr, err := journalbin.OpenDir(dir); err == nil {
				_ = mr.Iter(func(*journal.Event) bool { return true })
				_ = mr.Close()
			}

			_, _ = journalbin.Verify(path, key)
		})
	})
}

// FuzzJournalBinaryWriterReopen hands arbitrary bytes to the writer as
// an existing journal, the way slinit-journald reopens today's file
// after a crash, then appends to it and reads it back. The writer must
// refuse the file or append to it; it must not panic, hang or allocate
// without bound, nor leave a file the reader then panics or hangs on.
func FuzzJournalBinaryWriterReopen(f *testing.F) {
	for _, s := range journalSeeds(f) {
		f.Add(s, false)
		f.Add(s, true)
	}
	key := fuzzFSSKey(f)

	f.Fuzz(func(t *testing.T, data []byte, sealed bool) {
		path := filepath.Join(t.TempDir(), "system.journal")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Skip()
		}
		opts := journalbin.WriterOptions{BootID: "0123456789abcdef0123456789abcdef"}
		if sealed {
			opts.FSSKey = key
			opts.TagEvery = 2
		}
		runBounded(t, journalFuzzAllocBudget(len(data)), func() {
			w, err := journalbin.NewWriterWithOptions(path, opts)
			if err != nil {
				return
			}
			for i := 0; i < 3; i++ {
				ev := &journal.Event{Ts: int64(i+1) * 1e9, Msg: fmt.Sprintf("after reopen %d", i)}
				if _, err := w.Append(ev); err != nil {
					break
				}
			}
			_ = w.Close()
			if r, err := journalbin.OpenReader(path); err == nil {
				_ = r.Iter(func(*journal.Event) bool { return true })
				_, _ = r.EntryOffsets()
				_ = r.Close()
			}
		})
	})
}

func fuzzFSSKey(f *testing.F) *journalbin.FSSKey {
	f.Helper()
	k, err := journalbin.NewFSSKey(0, journalbin.DefaultFSSEpochUsec)
	if err != nil {
		f.Fatal(err)
	}
	return k
}

// journalSeeds are writer-made files of the shapes the readers branch
// on: empty, one entry, several entry arrays, repeated DATA, sealed.
func journalSeeds(f *testing.F) [][]byte {
	f.Helper()
	dir := f.TempDir()
	key := fuzzFSSKey(f)
	mk := func(name string, n int, opts journalbin.WriterOptions) []byte {
		path := filepath.Join(dir, name)
		w, err := journalbin.NewWriterWithOptions(path, opts)
		if err != nil {
			f.Fatal(err)
		}
		for i := 0; i < n; i++ {
			ev := &journal.Event{
				Ts:   int64(i+1) * 1e9,
				Mts:  int64(i+1) * 1e6,
				Msg:  fmt.Sprintf("seed %d", i%3),
				Unit: "svc",
				Pid:  100 + i%2,
			}
			if _, err := w.Append(ev); err != nil {
				f.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			f.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		return b
	}
	return [][]byte{
		mk("empty.journal", 0, journalbin.WriterOptions{}),
		mk("one.journal", 1, journalbin.WriterOptions{}),
		mk("many.journal", 9, journalbin.WriterOptions{BootID: "0123456789abcdef0123456789abcdef"}),
		mk("sealed.journal", 5, journalbin.WriterOptions{FSSKey: key, TagEvery: 2}),
		[]byte("SLJRNL01"),
	}
}
