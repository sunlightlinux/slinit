package main

import (
	"bytes"
	"fmt"
	"strings"
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
