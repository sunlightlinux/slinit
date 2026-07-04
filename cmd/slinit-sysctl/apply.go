package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// applyResult tallies per-pass counts so the CLI can emit one
// summary line under --verbose and pick an exit code that matches
// systemd-sysctl's semantics: non-zero only when at least one
// non-ignored spec failed.
type applyResult struct {
	applied  int
	ignored  int // errors swallowed because of `-` prefix
	errors   []error
}

func (r *applyResult) String() string {
	return fmt.Sprintf("applied=%d ignored=%d errors=%d",
		r.applied, r.ignored, len(r.errors))
}

// applySpec writes value+"\n" to /proc/sys/<key>. When strict is
// true, dash-prefix ignoreErrors is disregarded so operators can
// audit a config file for stale keys.
func applySpec(s spec, strict bool) error {
	path := filepath.Join(procSysRoot, s.key)
	// Trailing newline mirrors how sysctl(8) writes so any parser
	// on the other end of a pipe reads a well-formed line.
	err := os.WriteFile(path, []byte(s.value+"\n"), 0)
	if err == nil {
		return nil
	}
	if s.ignoreErrors && !strict {
		return errIgnored{path: path, cause: err}
	}
	return fmt.Errorf("%s = %s (from %s:%d): %w",
		s.rawKey, s.value, s.source, s.sourceLineNo, err)
}

// errIgnored marks a write that failed on a `-`-prefixed key. The
// CLI counts these separately from real errors so a verbose summary
// still shows the operator what was skipped.
type errIgnored struct {
	path  string
	cause error
}

func (e errIgnored) Error() string {
	return fmt.Sprintf("ignored: %s: %v", e.path, e.cause)
}

// applyFiles reads paths in order and applies every spec. File
// iteration order is preserved so later-in-list files can overwrite
// earlier ones (the discover() output is already ordered late-wins,
// so this composes cleanly).
func applyFiles(paths []string, strict, verbose bool) *applyResult {
	res := &applyResult{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			res.errors = append(res.errors, fmt.Errorf("open %s: %w", p, err))
			continue
		}
		specs, err := parseFile(f, p)
		f.Close()
		if err != nil {
			res.errors = append(res.errors, err)
			continue
		}
		for _, s := range specs {
			if err := applySpec(s, strict); err != nil {
				if _, ig := err.(errIgnored); ig {
					res.ignored++
					if verbose {
						fmt.Fprintln(os.Stderr, err)
					}
					continue
				}
				res.errors = append(res.errors, err)
				continue
			}
			res.applied++
		}
	}
	return res
}
