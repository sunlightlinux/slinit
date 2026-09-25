// slinit-systemd-convert converts a systemd .service unit file
// into a slinit service file (dinit-compatible text format).
//
// Handles the .service surface only — .timer / .socket / .path /
// .mount / .swap / .target units have their own semantics that
// need slinit-native equivalents (path activation directives,
// cron= for timers, boot service graph for targets) rather than
// mechanical translation. Warn and skip if pointed at one of those.
//
// Directive coverage: about 40 [Service] and [Unit] settings
// (User, Group, ExecStart, Restart, EnvironmentFile, PIDFile,
// LimitNOFILE, CapabilityBoundingSet, ProtectSystem, After,
// Requires, Wants, ConditionPathExists, etc.). Everything else
// generates a WARN so the operator knows what needs manual review.
//
// Usage:
//
//	slinit-systemd-convert /etc/systemd/system/foo.service > /etc/slinit.d/foo
//	slinit-systemd-convert --output-dir=/etc/slinit.d /usr/lib/systemd/system/*.service
//	slinit-systemd-convert --dry-run --verbose foo.service
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/config"
)

func main() {
	var (
		outputDir string
		dryRun    bool
		verbose   bool
	)
	flag.StringVar(&outputDir, "output-dir", "", "batch mode: write one slinit file per input into DIR")
	flag.BoolVar(&dryRun, "dry-run", false, "print what would be written without touching the filesystem")
	flag.BoolVar(&verbose, "verbose", false, "print per-service conversion notes to stderr")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `slinit-systemd-convert — port a systemd .service unit to a slinit service file

Usage:
  slinit-systemd-convert [flags] <unit-file> [unit-file ...]

Only .service units are supported. Timer/socket/path/mount/target
units are systemd-specific abstractions that map onto different
slinit facilities (directives, boot graph) — convert those by hand.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	inputs := flag.Args()
	if outputDir == "" && len(inputs) > 1 {
		fmt.Fprintln(os.Stderr, "slinit-systemd-convert: multiple inputs require --output-dir")
		os.Exit(1)
	}
	if outputDir != "" && !dryRun {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-systemd-convert: create output dir: %v\n", err)
			os.Exit(1)
		}
	}

	var hadErrors bool
	for _, in := range inputs {
		cfg, warns, err := config.ConvertSystemdUnit(in)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit-systemd-convert: %s: %v\n", in, err)
			hadErrors = true
			continue
		}
		// Strip .service (and .in) suffix from output name — slinit
		// svcs use bare names (`nginx`, not `nginx.service`).
		name := strings.TrimSuffix(filepath.Base(in), ".in")
		name = strings.TrimSuffix(name, ".service")

		var buf bytes.Buffer
		config.EmitSlinitFile(&buf, cfg)

		if outputDir == "" {
			os.Stdout.Write(buf.Bytes())
		} else {
			outPath := filepath.Join(outputDir, name)
			if dryRun {
				fmt.Fprintf(os.Stderr, "--- would write %s ---\n", outPath)
				os.Stderr.Write(buf.Bytes())
			} else if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-systemd-convert: write %s: %v\n", outPath, err)
				hadErrors = true
				continue
			}
		}

		if verbose {
			for _, w := range warns {
				fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", w.Level, name, w.Msg)
			}
		}
	}
	if hadErrors {
		os.Exit(1)
	}
}

