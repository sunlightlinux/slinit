// slinit-systemd-convert converts a systemd .service unit file
// into a slinit service file (dinit-compatible text format).
//
// Handles the .service surface only — .timer / .socket / .path /
// .mount / .swap / .target units have their own semantics that
// need slinit-native equivalents (path activation directives,
// cron= for timers, boot service graph for targets) rather than
// mechanical translation. Warn and skip if pointed at one of those.
//
// Every directive systemd defines for a .service unit is either mapped,
// or reported with what to do instead (pkg/config's directive registry,
// checked against systemd's own table). The unit's drop-ins
// (foo.service.d/*.conf, as `systemctl edit` writes them) are applied
// on top, from the systemd unit directories and the unit's own one.
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

	"github.com/sunlightlinux/slinit/internal/atomicfile"
	"github.com/sunlightlinux/slinit/pkg/config"
)

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// --version, alone: checked before anything else runs, so no other
	// argument changes meaning. The value is stamped by the package
	// build (-X main.version=...).
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-systemd-convert version %s\n", version)
		return
	}
	var (
		outputDir string
		dryRun    bool
		verbose   bool
		noDropins bool
		unitDirs  multiFlag
	)
	flag.StringVar(&outputDir, "output-dir", "", "batch mode: write one slinit file per input into DIR")
	flag.BoolVar(&dryRun, "dry-run", false, "print what would be written without touching the filesystem")
	flag.BoolVar(&verbose, "verbose", false, "also print conversion notes to stderr (warnings are always printed)")
	flag.BoolVar(&noDropins, "no-dropins", false, "convert the unit file alone, without its drop-ins")
	flag.Var(&unitDirs, "unit-dir", "a unit directory to search for drop-ins, highest priority first; repeatable\n(default: the unit's own directory, then "+strings.Join(config.DefaultSystemdDirs, ", ")+")")
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
		var dirs []string
		if !noDropins {
			dirs = dropinDirs(in, unitDirs)
		}
		cfg, warns, err := config.ConvertSystemdUnitIn(in, dirs)
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
			} else if err := atomicfile.Write(outPath, buf.Bytes(), 0o644, true); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-systemd-convert: write %s: %v\n", outPath, err)
				hadErrors = true
				continue
			}
		}

		// WARN is printed always, as slinit-immortal-convert does; NOTE
		// needs --verbose. A warning that has to be asked for is not a
		// warning: a plain conversion that lost something said nothing.
		for _, w := range warns {
			if w.Level == "WARN" || verbose {
				fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", w.Level, name, w.Msg)
			}
		}
	}
	if hadErrors {
		os.Exit(1)
	}
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// dropinDirs returns the directories searched for in's drop-ins. Given
// --unit-dir, those, in the order given. Otherwise systemd's unit
// directories, with the unit's own directory first when it is not one of
// them: converting a copy of a unit next to its foo.service.d should
// apply that directory's drop-ins, and an override in /etc should still
// apply to a unit converted from /usr/lib.
func dropinDirs(in string, given []string) []string {
	if len(given) > 0 {
		return given
	}
	own, err := filepath.Abs(filepath.Dir(in))
	if err != nil {
		return config.DefaultSystemdDirs
	}
	for _, d := range config.DefaultSystemdDirs {
		if d == own {
			return config.DefaultSystemdDirs
		}
	}
	return append([]string{own}, config.DefaultSystemdDirs...)
}
