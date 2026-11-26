package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journald"
)

const (
	testBoot1 = "11111111111111111111111111111111"
	testBoot2 = "22222222222222222222222222222222"
)

// writeTwoBootJournal writes, with the real sink of the given format,
// five entries of boot 1, a rotation, then five entries of boot 2 — two
// files, one boot each — and returns the files as -D finds them.
func writeTwoBootJournal(t *testing.T, format string) []string {
	t.Helper()
	dir := t.TempDir()
	var s interface {
		Handle(*journal.Event) error
		Rotate() error
		Close() error
	}
	var err error
	if format == "binary" {
		s, err = journald.NewBinarySink(journald.BinarySinkOptions{Dir: dir})
	} else {
		s, err = journald.NewFileSink(dir, 1)
	}
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	write := func(boot string, n int, at time.Time) {
		for i := 0; i < 5; i++ {
			e := &journal.Event{Ts: at.Add(time.Duration(i) * time.Minute).UnixNano(), Mts: int64(i+1) * 1e9,
				BootID: boot, Unit: "a.service", SyslogIdentifier: "a", Msg: fmt.Sprintf("boot%d msg %d", n, i),
				Prio: journal.PriorityInfo}
			if err := s.Handle(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(testBoot1, 1, base)
	if err := s.Rotate(); err != nil {
		t.Fatal(err)
	}
	write(testBoot2, 2, base.Add(time.Hour))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := journalFilesUnder(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("journal files = %q, %v; want 2", files, err)
	}
	return files
}

func queryFiles(t *testing.T, files []string, opts options) (string, error) {
	t.Helper()
	if opts.format == "" {
		opts.format = fmtCat
	}
	var out bytes.Buffer
	err := runFromFiles(files, opts, &out)
	return out.String(), err
}

// TestDirectoryQueriedAsOneJournal — -D used to run each file on its
// own: -n 3 printed three entries per file, -r reversed each file, -b
// -1 and -b 0 printed every boot, --list-boots printed entries, a
// malformed cursor was ignored and --show-cursor printed nothing.
func TestDirectoryQueriedAsOneJournal(t *testing.T) {
	for _, format := range []string{"binary", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			files := writeTwoBootJournal(t, format)
			cases := []struct {
				name string
				opts options
				want string
			}{
				{"tail", options{limit: 3}, "boot2 msg 2\nboot2 msg 3\nboot2 msg 4\n"},
				{"reverse", options{limit: 2, reverse: true}, "boot2 msg 4\nboot2 msg 3\n"},
				{"previous boot", options{bootSet: true, bootID: "-1", limit: 2}, "boot1 msg 3\nboot1 msg 4\n"},
				{"boot 0", options{bootSet: true, bootID: "0", limit: 6}, "boot2 msg 0\nboot2 msg 1\nboot2 msg 2\nboot2 msg 3\nboot2 msg 4\n"},
				{"bare -b", options{bootSet: true, limit: 1}, "boot2 msg 4\n"},
				{"boot id", options{bootSet: true, bootID: testBoot1, limit: 1}, "boot1 msg 4\n"},
				{"cursor", options{cursor: "s=0;b=" + testBoot2, limit: 1}, "boot2 msg 4\n"},
			}
			for _, c := range cases {
				got, err := queryFiles(t, files, c.opts)
				if err != nil || got != c.want {
					t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
				}
			}

			// The cursor of boot 1's last entry: inclusive starts at it,
			// --after-cursor at boot 2's first.
			got, err := queryFiles(t, files, options{bootSet: true, bootID: "-1", limit: 1, showCursor: true})
			if err != nil {
				t.Fatal(err)
			}
			_, cur, ok := strings.Cut(got, "-- cursor: ")
			if !ok || !strings.Contains(cur, "b="+testBoot1) {
				t.Fatalf("--show-cursor: %q", got)
			}
			cur = strings.TrimSpace(cur)
			if got, _ := queryFiles(t, files, options{cursor: cur, limit: 0}); !strings.HasPrefix(got, "boot1 msg 4\nboot2 msg 0\n") {
				t.Errorf("-c: %q", got)
			}
			if got, _ := queryFiles(t, files, options{afterCursor: cur}); !strings.HasPrefix(got, "boot2 msg 0\n") || strings.Count(got, "\n") != 5 {
				t.Errorf("--after-cursor: %q", got)
			}

			got, err = queryFiles(t, files, options{listBoots: true})
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			if len(lines) != 2 || !strings.HasPrefix(lines[0], " -1 "+testBoot1) || !strings.HasPrefix(lines[1], "  0 "+testBoot2) {
				t.Errorf("--list-boots: %q", got)
			}

			for _, bad := range []options{
				{cursor: "garbage"},
				{afterCursor: "s=1;b=abababababababababababababababab"},
				{bootSet: true, bootID: "-2"},
				{bootSet: true, bootID: "+1"},
			} {
				if out, err := queryFiles(t, files, bad); err == nil {
					t.Errorf("%+v: want an error, got none and %q", bad, out)
				}
			}
		})
	}
}

// TestFileQueryPicksBoot — --file goes through the same path: a single
// file holding one boot, -b selects it.
func TestFileQueryPicksBoot(t *testing.T) {
	files := writeTwoBootJournal(t, "jsonl")
	got, err := queryFiles(t, files[1:], options{bootSet: true, limit: 1})
	if err != nil || got != "boot2 msg 4\n" {
		t.Errorf("got %q, %v", got, err)
	}
}

// writeJSONL writes events to a JSONL file at path.
func writeJSONL(t *testing.T, path string, evs ...*journal.Event) {
	t.Helper()
	var b bytes.Buffer
	for _, e := range evs {
		line, err := e.MarshalJSONL()
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDirectoryTailAcrossFilesAndBoots — -n keeps the last N of each
// boot as the files are read, then merges them: one boot's entries
// interleaved over two files come out in monotonic order, and a newest
// boot with fewer than N matches is topped up from the one before.
func TestDirectoryTailAcrossFilesAndBoots(t *testing.T) {
	dir := t.TempDir()
	ev := func(boot string, mts int, unit string) *journal.Event {
		return &journal.Event{Ts: time.Now().UnixNano(), Mts: int64(mts) * 1e9, BootID: boot,
			Unit: unit, Msg: fmt.Sprintf("%s %d", boot[:1], mts), Prio: 6}
	}
	writeJSONL(t, dir+"/a.jsonl", ev(testBoot1, 1, "x"), ev(testBoot1, 3, "x"), ev(testBoot1, 5, "x"),
		ev(testBoot2, 2, "y"), ev(testBoot2, 4, "x"))
	writeJSONL(t, dir+"/b.jsonl", ev(testBoot1, 2, "x"), ev(testBoot1, 4, "x"), ev(testBoot1, 6, "x"),
		ev(testBoot2, 1, "x"), ev(testBoot2, 3, "y"))
	files, err := journalFilesUnder(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		opts options
		want string
	}{
		{options{limit: 3}, "2 2\n2 3\n2 4\n"},
		{options{limit: 4, units: []string{"x"}}, "1 5\n1 6\n2 1\n2 4\n"},
		{options{limit: 3, reverse: true, units: []string{"x"}}, "2 4\n2 1\n1 6\n"},
		{options{bootSet: true, bootID: "-1", limit: 4}, "1 3\n1 4\n1 5\n1 6\n"},
		{options{units: []string{"y"}}, "2 2\n2 3\n"},
	}
	for _, c := range cases {
		if got, err := queryFiles(t, files, c.opts); err != nil || got != c.want {
			t.Errorf("%+v: got %q, %v; want %q", c.opts, got, err, c.want)
		}
	}
}

// TestDirectoryQueryHoldsOnlyTheTail — fd29fc9 read every entry of every
// file into memory before selecting: -n 3 over a 190 MB journal took
// 353 MB. Only the entries that can be printed are held now. The heap is
// sampled while a -n 3 query reads ~12 MB of JSONL; holding it all puts
// the peak far above the file size, the tail keeps it to a few MB.
func TestDirectoryQueryHoldsOnlyTheTail(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 12 MB journal")
	}
	dir := t.TempDir()
	f, err := os.Create(dir + "/big.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	pad := strings.Repeat("p", 200)
	size := 0
	for i := 0; size < 12<<20; i++ {
		line, _ := (&journal.Event{Ts: int64(i + 1), Mts: int64(i + 1), BootID: testBoot1, Unit: "a.service",
			Msg: fmt.Sprintf("m %d %s", i, pad), Prio: 6}).MarshalJSONL()
		w.Write(line)
		w.WriteByte('\n')
		size += len(line) + 1
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var ms runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak.Load() {
				peak.Store(ms.HeapAlloc)
			}
		}
	}()
	got, err := queryFiles(t, []string{dir + "/big.jsonl"}, options{limit: 3})
	close(stop)
	<-done
	if err != nil || strings.Count(got, "\n") != 3 {
		t.Fatalf("got %d lines, %v", strings.Count(got, "\n"), err)
	}
	grew := int64(peak.Load()) - int64(base)
	t.Logf("heap grew %d KB reading %d KB", grew>>10, size>>10)
	if grew > int64(size)/3 {
		t.Errorf("heap grew %d KB reading a %d KB journal for -n 3; the entries were held", grew>>10, size>>10)
	}
}

// A container's or an image's journal directory may hold a symlink to a
// host journal file; -M and --image read only its regular files.
func TestForeignDirSkipsSymlinkedFiles(t *testing.T) {
	files := writeTwoBootJournal(t, "jsonl")
	dir := t.TempDir()
	if err := os.Symlink(files[0], dir+"/host.jsonl"); err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = out
	err = runFromDirectory(options{directory: dir, foreignDir: true, quiet: true})
	os.Stdout = stdout
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out.Name()); strings.Contains(string(b), "msg") {
		t.Errorf("a symlinked file in a foreign journal directory was read:\n%s", b)
	}
}
