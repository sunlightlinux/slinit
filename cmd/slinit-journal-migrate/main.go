// slinit-journal-migrate reads existing pkg/journald JSONL files
// (plain or .gz) from a directory and writes a fresh binary journal
// under the target directory, named after the current UTC day. The
// run is atomic (temporary file renamed into place) and refuses to
// touch an existing YYYY-MM-DD.journal, so it never duplicates events
// or writes into a file a running slinit-journald owns.
//
// Wire:
//
//	slinit-journal-migrate --from /var/log/slinit-journal \
//	                       --to   /var/log/slinit-journal-bin
//
// A --dry-run flag prints the migration plan (files + event count)
// without writing.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
	"github.com/sunlightlinux/slinit/pkg/journald"
)

var version = "dev"

func main() {
	var (
		from        = flag.String("from", journald.DefaultJournalDir, "source directory holding JSONL files")
		to          = flag.String("to", "", "destination directory for the binary journal (required)")
		dryRun      = flag.Bool("dry-run", false, "print the migration plan without writing")
		fsyncEvery  = flag.Int("fsync-every", 128, "fsync the destination journal every N events")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: slinit-journal-migrate --from DIR --to DIR [flags]

Migrates slinit-journald JSONL history into the new binary format.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *to == "" {
		fmt.Fprintln(os.Stderr, "slinit-journal-migrate: --to is required")
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*from, *to, *dryRun, *fsyncEvery); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journal-migrate: %v\n", err)
		os.Exit(1)
	}
}

// journalWriter is the part of *journalbin.Writer that run uses. It is
// an interface so tests can make Close fail: Close does the final
// header write and fsync, and an error there means the destination is
// not durable.
type journalWriter interface {
	Append(evt *journal.Event) (uint64, error)
	Flush() error
	Close() error
}

// newWriter opens the binary journal run writes into. Replaced in tests.
var newWriter = func(path string) (journalWriter, error) {
	// bootID / machineID left blank — the migrated events already
	// carry their original IDs in the per-event fields.
	return journalbin.NewWriter(path, "", "")
}

// maxLineSize bounds one JSONL line. A longer line cannot hold a valid
// event (MaxEventSize plus room for the JSON framing); it is skipped
// like any other unparseable line instead of aborting the run.
const maxLineSize = journal.MaxEventSize + 1024

// run enumerates JSONL files under `from`, opens each (decompressing
// .gz transparently), and writes every parseable event into a fresh
// binary journal at `to`. Missing bootID/machineID default to
// zeroes; callers migrating a live system usually run this once
// under root during a maintenance window.
//
// The run is all or nothing: events go into a hidden temporary file in
// `to` that is only renamed to YYYY-MM-DD.journal once it has been
// written, closed and fsynced. A failed run leaves no journal behind,
// so it can simply be run again. An existing YYYY-MM-DD.journal is
// never appended to or replaced: it may be a previous migration (the
// events would be written twice) or the file a running
// slinit-journald writes into.
func run(from, to string, dryRun bool, fsyncEvery int) (err error) {
	entries, err := os.ReadDir(from)
	if err != nil {
		return fmt.Errorf("read source %s: %w", from, err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.gz") {
			files = append(files, filepath.Join(from, name))
		}
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "slinit-journal-migrate: no .jsonl(.gz) files under %s — nothing to do\n", from)
		return nil
	}
	sortSourceFiles(files)

	if dryRun {
		fmt.Fprintf(os.Stderr, "slinit-journal-migrate: plan: %d files\n", len(files))
		for _, f := range files {
			fmt.Fprintf(os.Stderr, "  %s\n", f)
		}
		return nil
	}

	if err := os.MkdirAll(to, 0755); err != nil {
		return fmt.Errorf("mkdir dest %s: %w", to, err)
	}
	name := migrationTargetName()
	destPath := filepath.Join(to, name)
	if _, err := os.Lstat(destPath); err == nil {
		return fmt.Errorf("dest %s already exists; refusing to append to it (point --to at an empty directory)", destPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat dest %s: %w", destPath, err)
	}

	// The temporary name starts with a dot and does not end in
	// ".journal", so neither slinit-journalctl nor slinit-journald's
	// vacuum ever picks up a half-written file.
	tmp, err := os.CreateTemp(to, "."+name+".migrate-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", to, err)
	}
	tmpPath := tmp.Name()
	cerr := tmp.Chmod(0640)
	if err := tmp.Close(); cerr == nil {
		cerr = err
	}
	var w journalWriter
	defer func() {
		if err != nil {
			if w != nil {
				_ = w.Close()
			}
			_ = os.Remove(tmpPath)
		}
	}()
	if cerr != nil {
		return fmt.Errorf("prepare temp %s: %w", tmpPath, cerr)
	}
	w, err = newWriter(tmpPath)
	if err != nil {
		w = nil
		return fmt.Errorf("open dest %s: %w", tmpPath, err)
	}

	fsyncNext := fsyncEvery
	written, skipped := 0, 0
	var lineBuf []byte
	for _, f := range files {
		r, err := openJSONL(f)
		if err != nil {
			return fmt.Errorf("open %s: %w", f, err)
		}
		br := bufio.NewReaderSize(r, 64*1024)
		for {
			line, tooLong, rerr := readLine(br, lineBuf[:0], maxLineSize)
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				r.Close()
				return fmt.Errorf("read %s: %w", f, rerr)
			}
			lineBuf = line
			if tooLong {
				skipped++
				continue
			}
			if len(line) == 0 {
				continue
			}
			evt, err := journal.UnmarshalEvent(line)
			if err != nil {
				skipped++
				continue
			}
			if _, err := w.Append(evt); err != nil {
				r.Close()
				return fmt.Errorf("append from %s: %w", f, err)
			}
			written++
			if fsyncEvery > 0 && written >= fsyncNext {
				if err := w.Flush(); err != nil {
					r.Close()
					return fmt.Errorf("flush: %w", err)
				}
				fsyncNext += fsyncEvery
			}
		}
		r.Close()
	}

	// Close writes the final header and fsyncs; until it succeeds the
	// journal is not durable, so its error is the run's error.
	cw := w
	w = nil
	if err := cw.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := renameNoReplace(tmpPath, destPath); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpPath, destPath, err)
	}
	if err := syncDir(to); err != nil {
		// The file is complete and in place; only the directory entry
		// may not have reached the disk yet. Report it, keep the file.
		return fmt.Errorf("fsync dir %s: %w", to, err)
	}
	fmt.Fprintf(os.Stderr, "slinit-journal-migrate: %s → %s (%d events written, %d skipped)\n",
		from, destPath, written, skipped)
	return nil
}

// readLine returns the next line from br without its trailing "\n"
// (and "\r"), reusing buf. A line longer than max is consumed to its
// end without being kept and reported with tooLong set, so a huge
// line costs at most max plus one buffer of memory. io.EOF is returned
// only when no line is left.
func readLine(br *bufio.Reader, buf []byte, max int) (line []byte, tooLong bool, err error) {
	buf = buf[:0]
	read := false
	for {
		chunk, rerr := br.ReadSlice('\n')
		if len(chunk) > 0 {
			read = true
		}
		if !tooLong {
			buf = append(buf, chunk...)
			if len(bytes.TrimSuffix(buf, []byte("\n"))) > max {
				tooLong = true
				buf = buf[:0]
			}
		}
		switch {
		case rerr == bufio.ErrBufferFull:
			continue
		case rerr == io.EOF:
			if !read {
				return buf, false, io.EOF
			}
		case rerr != nil:
			return buf, tooLong, rerr
		}
		if tooLong {
			return buf[:0], true, nil
		}
		buf = bytes.TrimSuffix(buf, []byte("\n"))
		return bytes.TrimSuffix(buf, []byte("\r")), false, nil
	}
}

// sortSourceFiles orders the source files oldest first. Day files
// ("YYYY-MM-DD.jsonl", rotated "YYYY-MM-DD.<ns>.jsonl") sort by name,
// but the shutdown tails PID 1 writes ("shutdown-YYYYMMDD-HHMMSS-<boot>
// .jsonl") would sort after every day file. A tail is placed after the
// day file it was written on and before the next day's.
func sortSourceFiles(files []string) {
	key := func(p string) string {
		base := filepath.Base(p)
		if d, ok := shutdownTailDay(base); ok {
			// "~" sorts after every character of a day file name.
			return d + "~" + base
		}
		return base
	}
	sort.SliceStable(files, func(i, j int) bool {
		ki, kj := key(files[i]), key(files[j])
		if ki != kj {
			return ki < kj
		}
		return files[i] < files[j]
	})
}

// shutdownTailDay maps "shutdown-YYYYMMDD-HHMMSS-..." to "YYYY-MM-DD".
func shutdownTailDay(base string) (string, bool) {
	rest, ok := strings.CutPrefix(base, "shutdown-")
	if !ok || len(rest) < 9 || rest[8] != '-' {
		return "", false
	}
	for _, c := range rest[:8] {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return rest[0:4] + "-" + rest[4:6] + "-" + rest[6:8], true
}

// renameNoReplace moves tmp to dest unless dest exists, atomically:
// renameat2(RENAME_NOREPLACE), or link+unlink where the filesystem
// lacks it. Plain rename would replace a journal created after the
// up-front check (a slinit-journald started meanwhile).
func renameNoReplace(tmp, dest string) error {
	err := unix.Renameat2(unix.AT_FDCWD, tmp, unix.AT_FDCWD, dest, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	if err := os.Link(tmp, dest); err != nil {
		return err
	}
	return os.Remove(tmp)
}

// syncDir fsyncs a directory so a rename in it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	if err := d.Close(); serr == nil {
		serr = err
	}
	return serr
}

// openJSONL opens a file, decompressing .gz transparently via
// pkg/journald.OpenCompressed. Callers close the returned Reader.
func openJSONL(path string) (io.ReadCloser, error) {
	if strings.HasSuffix(path, ".gz") {
		return journald.OpenCompressed(path)
	}
	return os.Open(path)
}

// migrationTargetName returns "YYYY-MM-DD.journal" for today. Match
// the binary sink's own naming so a subsequent slinit-journald
// startup opens this file directly.
func migrationTargetName() string {
	t := time.Now().UTC()
	return fmt.Sprintf("%04d-%02d-%02d.journal", t.Year(), int(t.Month()), t.Day())
}
