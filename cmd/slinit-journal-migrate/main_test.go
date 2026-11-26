package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

func jsonlLine(t *testing.T, msg string) string {
	t.Helper()
	b, err := (&journal.Event{Ts: 1_700_000_000_000_000_000, Mts: 1, Msg: msg}).MarshalJSONL()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(b), "\n") + "\n"
}

func readMsgs(t *testing.T, path string) []string {
	t.Helper()
	r, err := journalbin.OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer r.Close()
	var msgs []string
	if err := r.Iter(func(e *journal.Event) bool {
		msgs = append(msgs, e.Msg)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return msgs
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// A line longer than the line limit used to stop bufio.Scanner with
// ErrTooLong: the run failed with half the events already in the
// destination. It is now skipped and counted like any unparseable line.
func TestOverlongLineIsSkipped(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	huge := `{"ts":1,"msg":"` + strings.Repeat("x", maxLineSize+10) + `"}` + "\n"
	body := jsonlLine(t, "before") + huge + jsonlLine(t, "after")
	if err := os.WriteFile(filepath.Join(from, "2026-11-01.jsonl"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if err := run(from, to, false, 1); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := readMsgs(t, filepath.Join(to, migrationTargetName()))
	if want := []string{"before", "after"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

type failingCloseWriter struct{ journalWriter }

func (f failingCloseWriter) Close() error {
	_ = f.journalWriter.Close()
	return errors.New("injected fsync failure")
}

// Close does the final header write + fsync. Its error used to be
// printed from a defer while run returned nil (exit 0).
func TestCloseErrorFailsRunAndLeavesNothing(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "2026-11-01.jsonl"), []byte(jsonlLine(t, "a")), 0644); err != nil {
		t.Fatal(err)
	}
	orig := newWriter
	t.Cleanup(func() { newWriter = orig })
	newWriter = func(path string) (journalWriter, error) {
		w, err := orig(path)
		if err != nil {
			return nil, err
		}
		return failingCloseWriter{w}, nil
	}
	err := run(from, to, false, 0)
	if err == nil || !strings.Contains(err.Error(), "injected fsync failure") {
		t.Fatalf("run err = %v, want the Close error", err)
	}
	if names := dirNames(t, to); len(names) != 0 {
		t.Fatalf("failed run left %q in the destination", names)
	}
}

// A run that fails part way (here: an unreadable source file after a
// good one) leaves no journal, so a re-run cannot duplicate events.
func TestFailedRunLeavesNothing(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "2026-11-01.jsonl"), []byte(jsonlLine(t, "a")), 0644); err != nil {
		t.Fatal(err)
	}
	// A directory named like a source file: open succeeds, read fails.
	if err := os.Mkdir(filepath.Join(from, "2026-11-02.jsonl"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := run(from, to, false, 1); err == nil {
		t.Fatal("run succeeded reading a directory")
	}
	if names := dirNames(t, to); len(names) != 0 {
		t.Fatalf("failed run left %q in the destination", names)
	}
}

// An existing day journal is neither appended to (events twice) nor
// replaced.
func TestExistingTargetRefused(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "2026-11-01.jsonl"), []byte(jsonlLine(t, "a")), 0644); err != nil {
		t.Fatal(err)
	}
	if err := run(from, to, false, 0); err != nil {
		t.Fatalf("first run: %v", err)
	}
	dest := filepath.Join(to, migrationTargetName())
	before, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(from, to, false, 0); err == nil {
		t.Fatal("second run into the same day file succeeded")
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("second run modified the existing journal")
	}
	if got := readMsgs(t, dest); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("events = %q", got)
	}
	if names := dirNames(t, to); len(names) != 1 {
		t.Fatalf("destination holds %q, want only the journal", names)
	}
}

func TestSortSourceFilesPlacesShutdownTails(t *testing.T) {
	files := []string{
		"/d/shutdown-20261102-235900-abcd1234.jsonl",
		"/d/2026-11-03.jsonl",
		"/d/2026-11-02.jsonl",
		"/d/2026-11-02.1730000000.jsonl.gz",
		"/d/shutdown-20261101-120000-abcd1234.jsonl",
		"/d/2026-11-01.jsonl",
	}
	sortSourceFiles(files)
	want := []string{
		"/d/2026-11-01.jsonl",
		"/d/shutdown-20261101-120000-abcd1234.jsonl",
		"/d/2026-11-02.1730000000.jsonl.gz",
		"/d/2026-11-02.jsonl",
		"/d/shutdown-20261102-235900-abcd1234.jsonl",
		"/d/2026-11-03.jsonl",
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("order:\n got %q\nwant %q", files, want)
	}
}

func TestReadLine(t *testing.T) {
	// Covered through run above; here the edge cases: CRLF, no final
	// newline, an over-long final line without newline.
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	long := strings.Repeat("y", 100)
	if err := os.WriteFile(p, []byte("a\r\n\nb\n"+long), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 16) // lines span several ReadSlice chunks
	type res struct {
		line    string
		tooLong bool
	}
	var got []res
	for {
		line, tl, err := readLine(br, nil, 50)
		if err != nil {
			break
		}
		got = append(got, res{string(line), tl})
	}
	want := []res{{"a", false}, {"", false}, {"b", false}, {"", true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
