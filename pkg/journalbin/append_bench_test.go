package journalbin

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// BenchmarkAppendLargeFile measures one append to a file that already
// holds 50k distinct messages. Every chain node in the DATA hash table
// used to be read in full before its hash was compared, so the cost of
// an append grew with the file (25us/event at 0-20k, 243us at 100-120k).
func BenchmarkAppendLargeFile(b *testing.B) {
	w, err := NewWriterWithOptions(filepath.Join(b.TempDir(), "big.journal"), WriterOptions{
		BootID: testBootID, MachineID: testMachineID, MaxSize: 128 << 20,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer w.Close()
	evt := func(i int) *journal.Event {
		return &journal.Event{Ts: int64(i+1) * 1000, Mts: int64(i+1) * 1000, Prio: journal.PriorityInfo,
			Msg: "message number " + strconv.Itoa(i), Unit: "svc", Transport: journal.TransportStdout, Pid: 42}
	}
	const prefill = 50_000
	for i := 0; i < prefill; i++ {
		if _, err := w.Append(evt(i)); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := w.Append(evt(prefill + i)); err != nil {
			b.Fatal(err)
		}
	}
}
