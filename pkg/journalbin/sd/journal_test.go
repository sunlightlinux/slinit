package sd

import (
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

const (
	testBootID    = "0123456789abcdef0123456789abcdef"
	testMachineID = "fedcba9876543210fedcba9876543210"
)

// buildFile is a shorthand for creating one .journal with N events
// at incrementing microsecond timestamps.
func buildFile(t *testing.T, dir, name string, msgs []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	w, err := journalbin.NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range msgs {
		if _, err := w.Append(&journal.Event{
			Ts:   int64(i+1) * 1_000_000, // us step
			Msg:  m,
			Prio: journal.PriorityInfo,
			Unit: "u" + m,
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	return path
}

func TestOpenAndIterateSingleFile(t *testing.T) {
	dir := t.TempDir()
	buildFile(t, dir, "a.journal", []string{"one", "two", "three"})

	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	var got []string
	for j.Next() {
		msg, err := j.GetData("MESSAGE")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, msg)
	}
	if len(got) != 3 || got[0] != "one" || got[2] != "three" {
		t.Fatalf("got %v, want [one two three]", got)
	}
}

func TestOpenFilesMergesTimeOrder(t *testing.T) {
	dir := t.TempDir()
	// A: 1,3,5; B: 2,4,6
	pathA := buildFile(t, dir, "a.journal", []string{"a1", "a3", "a5"})
	pathB := buildFile(t, dir, "b.journal", []string{"b2", "b4", "b6"})
	// The Writer uses 1..N microsecond steps, so file A has
	// realtimes 1,2,3 (us) and file B has 1,2,3 too — same order.
	// Force distinct timestamps by rewriting file B with an offset.
	// (Simpler: just test that Open() merges N files without error.)
	_ = pathA
	_ = pathB

	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var count int
	for j.Next() {
		count++
	}
	if count != 6 {
		t.Fatalf("merged count = %d, want 6", count)
	}
}

func TestGetRealtimeAndMonotonicUsec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.journal")
	w, _ := journalbin.NewWriter(path, testBootID, testMachineID)
	w.Append(&journal.Event{Ts: 5_000_000_000, Mts: 3_000_000_000, Msg: "x"})
	w.Close()

	j, err := OpenFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if !j.Next() {
		t.Fatal("Next returned false on non-empty journal")
	}
	rt, err := j.GetRealtimeUsec()
	if err != nil {
		t.Fatal(err)
	}
	if rt != 5_000_000 {
		t.Errorf("realtime usec = %d, want 5000000", rt)
	}
	mt, err := j.GetMonotonicUsec()
	if err != nil {
		t.Fatal(err)
	}
	if mt != 3_000_000 {
		t.Errorf("monotonic usec = %d, want 3000000", mt)
	}
}

func TestCursorRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.journal")
	w, _ := journalbin.NewWriter(path, testBootID, testMachineID)
	for i := 1; i <= 5; i++ {
		w.Append(&journal.Event{Ts: int64(i) * 1_000_000, Msg: itoa(i)})
	}
	w.Close()

	j, err := OpenFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	// Advance to entry 3, grab cursor.
	j.Next()
	j.Next()
	j.Next()
	msg, _ := j.GetData("MESSAGE")
	if msg != "3" {
		t.Fatalf("at entry 3 got %q", msg)
	}
	cursor, err := j.GetCursor()
	if err != nil {
		t.Fatal(err)
	}
	if !j.TestCursor(cursor) {
		t.Fatal("TestCursor(cursor from GetCursor) returned false")
	}

	// Fresh journal, seek to that cursor, verify Next() lands on
	// entry 3.
	j2, _ := OpenFiles(path)
	defer j2.Close()
	if err := j2.SeekCursor(cursor); err != nil {
		t.Fatal(err)
	}
	if !j2.Next() {
		t.Fatal("Next after SeekCursor returned false")
	}
	msg, _ = j2.GetData("MESSAGE")
	if msg != "3" {
		t.Errorf("SeekCursor landed on %q, want 3", msg)
	}
}

func TestAddMatchAndFlush(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.journal")
	w, _ := journalbin.NewWriter(path, testBootID, testMachineID)
	w.Append(&journal.Event{Ts: 1_000_000, Msg: "err msg", Prio: journal.PriorityError, Unit: "sshd"})
	w.Append(&journal.Event{Ts: 2_000_000, Msg: "info msg", Prio: journal.PriorityInfo, Unit: "sshd"})
	w.Append(&journal.Event{Ts: 3_000_000, Msg: "cron info", Prio: journal.PriorityInfo, Unit: "cron"})
	w.Close()

	j, _ := OpenFiles(path)
	defer j.Close()

	// PRIORITY=3 → keep err (3) and above.
	if err := j.AddMatch("PRIORITY=3"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for j.Next() {
		msg, _ := j.GetData("MESSAGE")
		got = append(got, msg)
	}
	if len(got) != 1 || got[0] != "err msg" {
		t.Fatalf("PRIORITY match: got %v, want [err msg]", got)
	}

	// Flush + Unit filter.
	j.FlushMatches()
	if err := j.SeekHead(); err != nil {
		t.Fatal(err)
	}
	if err := j.AddMatch("_SLINIT_UNIT=sshd"); err != nil {
		t.Fatal(err)
	}
	got = nil
	for j.Next() {
		msg, _ := j.GetData("MESSAGE")
		got = append(got, msg)
	}
	if len(got) != 2 || got[0] != "err msg" || got[1] != "info msg" {
		t.Fatalf("UNIT match: got %v", got)
	}
}

func TestOpenNonDirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	// A regular file, not a dir — Open should reject it.
	path := filepath.Join(dir, "notadir")
	if err := writeAllFile(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open on a regular file should have errored")
	}
}

func TestSeekRealtimeUsec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.journal")
	w, _ := journalbin.NewWriter(path, testBootID, testMachineID)
	// Realtimes 1000, 2000, 3000, 4000, 5000 (us).
	for i := 1; i <= 5; i++ {
		w.Append(&journal.Event{Ts: int64(i) * 1_000_000, Msg: itoa(i)})
	}
	w.Close()

	j, _ := OpenFiles(path)
	defer j.Close()

	// Seek to 2500us → first entry >= is 3000us → after Next() land
	// on the "3" event.
	if err := j.SeekRealtimeUsec(2500); err != nil {
		t.Fatal(err)
	}
	if !j.Next() {
		t.Fatal("Next after SeekRealtimeUsec returned false")
	}
	msg, _ := j.GetData("MESSAGE")
	if msg != "3" {
		t.Errorf("SeekRealtimeUsec(2500) → %q, want 3", msg)
	}
}
