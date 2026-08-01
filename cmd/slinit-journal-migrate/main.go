// slinit-journal-migrate reads existing pkg/journald JSONL files
// (plain or .gz) from a directory and writes a fresh binary journal
// under the target directory. Idempotent-ish: overwriting into an
// existing binary directory appends to the current-day file rather
// than replaying past history; operators wanting a clean migration
// point at an empty target dir.
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
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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

// run enumerates JSONL files under `from`, opens each (decompressing
// .gz transparently), and writes every parseable event into a fresh
// binary journal at `to`. Missing bootID/machineID default to
// zeroes; callers migrating a live system usually run this once
// under root during a maintenance window.
func run(from, to string, dryRun bool, fsyncEvery int) error {
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
	sort.Strings(files) // deterministic order (filename date-sortable)

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
	destPath := filepath.Join(to, migrationTargetName())
	// Bootstrap the binary writer. bootID / machineID left blank —
	// the migrated events already carry their original IDs in the
	// per-event fields.
	w, err := journalbin.NewWriter(destPath, "", "")
	if err != nil {
		return fmt.Errorf("open dest %s: %w", destPath, err)
	}
	defer func() {
		if cerr := w.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "slinit-journal-migrate: close: %v\n", cerr)
		}
	}()

	fsyncNext := fsyncEvery
	written, skipped := 0, 0
	for _, f := range files {
		r, err := openJSONL(f)
		if err != nil {
			return fmt.Errorf("open %s: %w", f, err)
		}
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), journal.MaxEventSize+1024)
		for scanner.Scan() {
			line := scanner.Bytes()
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
		if err := scanner.Err(); err != nil {
			r.Close()
			return fmt.Errorf("scan %s: %w", f, err)
		}
		r.Close()
	}
	fmt.Fprintf(os.Stderr, "slinit-journal-migrate: %s → %s (%d events written, %d skipped)\n",
		from, destPath, written, skipped)
	return nil
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
