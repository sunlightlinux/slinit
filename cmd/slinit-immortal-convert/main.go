// slinit-immortal-convert converts an immortal service definition
// (`run.yml`, as used by immortal and immortaldir) into a slinit
// service file in the dinit-compatible text format.
//
// immortal supervises one service per process and configures it in a
// small flat YAML file, so almost every key has a slinit directive
// behind it. The ones that do not — immortal's own supervisor pidfile,
// following a pid it did not start — are reported on stderr rather than
// dropped, because a conversion that silently loses a line is worse
// than one that says what it could not do.
//
// Usage:
//
//	slinit-immortal-convert /usr/local/etc/immortal/www.yml > /etc/slinit.d/www
//	slinit-immortal-convert --output-dir=/etc/slinit.d /usr/local/etc/immortal/*.yml
//	slinit-immortal-convert --dry-run --verbose www.yml
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type warning struct {
	level string // NOTE / WARN
	msg   string
}

func main() {
	var (
		outputDir string
		dryRun    bool
		verbose   bool
	)
	flag.StringVar(&outputDir, "output-dir", "", "batch mode: write one slinit file per input into DIR (default: stdout)")
	flag.BoolVar(&dryRun, "dry-run", false, "print what would be written without touching the filesystem")
	flag.BoolVar(&verbose, "verbose", false, "print per-service conversion notes to stderr")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `slinit-immortal-convert — port an immortal run.yml to a slinit service file

Usage:
  slinit-immortal-convert [flags] <run.yml> [run.yml ...]

Single input mode (default):
  slinit-immortal-convert /usr/local/etc/immortal/www.yml > /etc/slinit.d/www

Batch mode (--output-dir), as immortaldir lays them out:
  slinit-immortal-convert --output-dir=/etc/slinit.d /usr/local/etc/immortal/*.yml

The service name comes from the file name without its extension, which
is how immortaldir names services too.

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
		fmt.Fprintln(os.Stderr, "slinit-immortal-convert: multiple inputs require --output-dir")
		os.Exit(1)
	}
	if outputDir != "" && !dryRun {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-immortal-convert: create output dir: %v\n", err)
			os.Exit(1)
		}
	}

	var hadErrors bool
	for _, in := range inputs {
		name := serviceName(in)

		f, err := os.Open(in)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit-immortal-convert: %s: %v\n", in, err)
			hadErrors = true
			continue
		}
		cfg, warns, err := parseImmortalYAML(bufio.NewScanner(f))
		f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit-immortal-convert: %s: %v\n", in, err)
			hadErrors = true
			continue
		}

		var buf bytes.Buffer
		warns = append(warns, emitSlinitFile(&buf, cfg, name)...)

		switch {
		case outputDir == "":
			os.Stdout.Write(buf.Bytes())
		case dryRun:
			fmt.Fprintf(os.Stderr, "--- would write %s ---\n", filepath.Join(outputDir, name))
			os.Stderr.Write(buf.Bytes())
		default:
			outPath := filepath.Join(outputDir, name)
			if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-immortal-convert: write %s: %v\n", outPath, err)
				hadErrors = true
			}
		}

		if verbose {
			for _, w := range warns {
				fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", w.level, name, w.msg)
			}
		}
	}

	if hadErrors {
		os.Exit(1)
	}
}

// serviceName mirrors immortaldir: the file name without its
// extension is the service name.
func serviceName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
