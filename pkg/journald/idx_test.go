package journald

import (
	"os"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

func TestIdxEncodeDecodeRoundtrip(t *testing.T) {
	orig := IdxRecord{TsUsec: 1_234_567_890, Offset: 4096}
	var buf [IdxRecordSize]byte
	encodeIdxRecord(buf[:], orig)
	got, err := decodeIdxRecord(buf[:])
	if err != nil {
		t.Fatal(err)
	}
	if got != orig {
		t.Fatalf("roundtrip: got %+v want %+v", got, orig)
	}
}

func TestIdxDecodeShortBuffer(t *testing.T) {
	if _, err := decodeIdxRecord(make([]byte, 8)); err == nil {
		t.Fatal("expected error for short buffer")
	}
}

func TestFileSinkWritesIdx(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileSink(dir, 1) // fsync every event
	if err != nil {
		t.Fatal(err)
	}

	events := []*journal.Event{
		{Ts: 1_000_000_000, Msg: "a"},
		{Ts: 2_000_000_000, Msg: "b"},
		{Ts: 3_000_000_000, Msg: "c"},
	}
	for _, e := range events {
		if err := fs.Handle(e); err != nil {
			t.Fatal(err)
		}
	}
	fs.Close()

	// The .idx should have exactly 3 records.
	idxR, err := OpenIdx(idxPath(fs.CurrentPath()))
	if err != nil {
		t.Fatal(err)
	}
	defer idxR.Close()
	if idxR.Len() != 3 {
		t.Fatalf("idx records: got %d want 3", idxR.Len())
	}
	// TsUsec must be ts_ns/1000, and offsets must be strictly
	// increasing and cover the whole file up to (but not equal to)
	// the file size.
	jsonlSt, _ := os.Stat(fs.CurrentPath())
	var prev int64 = -1
	for i := int64(0); i < 3; i++ {
		rec, err := idxR.At(i)
		if err != nil {
			t.Fatal(err)
		}
		if rec.TsUsec != events[i].Ts/1000 {
			t.Fatalf("idx[%d].TsUsec = %d, want %d", i, rec.TsUsec, events[i].Ts/1000)
		}
		if rec.Offset <= prev {
			t.Fatalf("idx[%d].Offset = %d, not > previous %d", i, rec.Offset, prev)
		}
		if rec.Offset >= jsonlSt.Size() {
			t.Fatalf("idx[%d].Offset = %d, past file end %d", i, rec.Offset, jsonlSt.Size())
		}
		prev = rec.Offset
	}
}

func TestIdxLowerBound(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileSink(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range []int64{100_000, 200_000, 300_000, 400_000, 500_000} {
		if err := fs.Handle(&journal.Event{Ts: ts * 1000}); err != nil {
			t.Fatal(err)
		}
	}
	fs.Close()

	idxR, err := OpenIdx(idxPath(fs.CurrentPath()))
	if err != nil {
		t.Fatal(err)
	}
	defer idxR.Close()

	cases := []struct {
		q     int64
		wantI int64
	}{
		{50_000, 0},   // before all
		{100_000, 0},  // exact first
		{150_000, 1},  // between 1 and 2
		{300_000, 2},  // exact middle
		{500_000, 4},  // exact last
		{600_000, 5},  // past all → Len
	}
	for _, c := range cases {
		got, err := idxR.LowerBound(c.q)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.wantI {
			t.Errorf("LowerBound(%d): got %d, want %d", c.q, got, c.wantI)
		}
	}
}

func TestOpenIdxRejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	badPath := dir + "/broken.idx"
	// 20 bytes = not a multiple of 16.
	if err := os.WriteFile(badPath, make([]byte, 20), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenIdx(badPath); err == nil {
		t.Fatal("expected error for non-multiple size")
	}
}

func TestRebuildIdx(t *testing.T) {
	// Write a jsonl file, delete its .idx, rebuild, verify.
	dir := t.TempDir()
	fs, err := NewFileSink(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	events := []*journal.Event{
		{Ts: 10_000_000, Msg: "x"},
		{Ts: 20_000_000, Msg: "y"},
	}
	for _, e := range events {
		fs.Handle(e)
	}
	fs.Close()

	// Nuke the idx and rebuild.
	if err := os.Remove(idxPath(fs.CurrentPath())); err != nil {
		t.Fatal(err)
	}
	count, err := RebuildIdx(fs.CurrentPath())
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("RebuildIdx count: got %d want 2", count)
	}
	idxR, err := OpenIdx(idxPath(fs.CurrentPath()))
	if err != nil {
		t.Fatal(err)
	}
	defer idxR.Close()
	if idxR.Len() != 2 {
		t.Fatalf("rebuilt idx len: got %d want 2", idxR.Len())
	}
	for i := int64(0); i < 2; i++ {
		rec, _ := idxR.At(i)
		if rec.TsUsec != events[i].Ts/1000 {
			t.Fatalf("rebuilt idx[%d].TsUsec = %d, want %d",
				i, rec.TsUsec, events[i].Ts/1000)
		}
	}
}

func TestPeekTsUsec(t *testing.T) {
	cases := []struct {
		line string
		want int64
		ok   bool
	}{
		{`{"ts":123000,"msg":"hi"}`, 123, true},          // 123000 ns → 123 us
		{`{"msg":"hi","ts":1000000}`, 1000, true},        // works mid-line
		{`{"ts": 500000, "msg":"hi"}`, 500, true},        // whitespace after colon
		{`{"ts":-2000,"msg":"neg"}`, -2, true},           // negative
		{`{"msg":"no ts here"}`, 0, false},
		{`{"ts":"not a number"}`, 0, false},
	}
	for _, c := range cases {
		got, err := peekTsUsec([]byte(c.line))
		if c.ok && err != nil {
			t.Errorf("peekTsUsec(%q) error: %v", c.line, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("peekTsUsec(%q) expected error", c.line)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("peekTsUsec(%q) got %d, want %d", c.line, got, c.want)
		}
	}
}
