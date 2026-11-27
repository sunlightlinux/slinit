package main

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

// BenchmarkDedupLargeJournal: a boot whose journal holds many entries
// clients sent straight to events.sock, and a small backlog at its end.
// The replay must not read and decode all of them before the daemon
// binds its socket (the second review measured 0.86 s and 429 MB for
// 300k entries before the time filter and the early stop).
func BenchmarkDedupLargeJournal(b *testing.B) {
	dir := b.TempDir()
	w, err := journalbin.NewWriter(filepath.Join(dir, "day.journal"), testBoot, testMach)
	if err != nil {
		b.Fatal(err)
	}
	const n = 100000
	for i := 0; i < n; i++ {
		if _, err := w.Append(ev(int64(i+1)*1000, fmt.Sprintf("client %d", i))); err != nil {
			b.Fatal(err)
		}
	}
	ring := []*journal.Event{ev(int64(n-1)*1000, fmt.Sprintf("client %d", n-2)), ev(int64(n)*1000, fmt.Sprintf("client %d", n-1))}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	f := dedupAgainstDisk(dir, testBoot)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := f(ring); len(out) != 0 {
			b.Fatalf("kept %d of a backlog that is all on disk", len(out))
		}
	}
}
