package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// applyResult tallies per-pass counts so the CLI can emit one
// summary line under --verbose and pick an exit code that matches
// systemd-sysctl's semantics: non-zero when a config line could not
// be parsed or at least one non-ignored write failed.
type applyResult struct {
	applied  int
	ignored  int      // failed writes that were not counted as errors
	warnings []string // ignored failures worth showing without --verbose
	errors   []error
}

func (r *applyResult) String() string {
	return fmt.Sprintf("applied=%d ignored=%d errors=%d",
		r.applied, r.ignored, len(r.errors))
}

// createMissing lets a write create the target file. Only the --root
// preview sets it, so a config can be previewed into an empty fixture
// tree; against the real /proc/sys a missing tunable must stay
// ENOENT, never "created".
var createMissing = false

// errIgnored marks a write failure that does not fail the pass: a
// `-`-prefixed key, a tunable this kernel lacks (ENOENT), or a write
// refused for permission (EROFS/EACCES/EPERM, how container managers
// protect sysctls). warn is set for the ENOENT case, which
// systemd-sysctl logs at warning level rather than debug.
type errIgnored struct {
	path  string
	cause error
	warn  bool
}

func (e errIgnored) Error() string {
	return fmt.Sprintf("ignored: %s: %v", e.path, e.cause)
}

// writeRefused mirrors systemd's ERRNO_IS_FS_WRITE_REFUSED.
func writeRefused(err error) bool {
	return errors.Is(err, syscall.EROFS) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM)
}

// classify turns a failed write into errIgnored or a hard error,
// following systemd-sysctl's sysctl_write_or_warn(): the `-` prefix
// and, outside --strict, a refused write are ignored quietly; outside
// --strict a missing tunable is ignored with a warning; anything else
// is an error. Unlike systemd, --strict here also disregards the `-`
// prefix (documented slinit behaviour, used to audit a config).
func classify(err error, s spec, path string, strict bool) error {
	if (s.ignoreErrors && !strict) || (!strict && writeRefused(err)) {
		return errIgnored{path: path, cause: err}
	}
	if !strict && errors.Is(err, fs.ErrNotExist) {
		return errIgnored{path: path, cause: err, warn: true}
	}
	return fmt.Errorf("%s = %s (from %s:%d): %w",
		s.rawKey, s.value, s.source, s.sourceLineNo, err)
}

// writeKey writes value+"\n" to procSysRoot/key. It never creates
// the file (unless the --root preview asked for it): a tunable is a
// file the kernel already exposes.
func writeKey(key, value string) (string, error) {
	path := filepath.Join(procSysRoot, key)
	flags := os.O_WRONLY | os.O_TRUNC
	if createMissing {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0644)
	if err != nil {
		return path, err
	}
	// Trailing newline mirrors how sysctl(8) writes so any parser
	// on the other end of a pipe reads a well-formed line.
	_, werr := f.WriteString(value + "\n")
	cerr := f.Close()
	if werr != nil {
		return path, werr
	}
	return path, cerr
}

// applySpec writes one literal (non-glob) assignment.
func applySpec(s spec, strict bool) error {
	path, err := writeKey(s.key, s.value)
	if err == nil {
		return nil
	}
	return classify(err, s, path, strict)
}

// optionSet is systemd-sysctl's ordered hashmap of options, keyed by
// the normalised key. Every file feeds the same set, so a later
// assignment of a key replaces an earlier one (and moves to the
// end), a `-key` exclusion cancels an earlier assignment of that
// key, and a glob never touches a key that has its own entry.
type optionSet struct {
	m   map[string]*spec
	seq map[string]int
	n   int
}

func newOptionSet() *optionSet {
	return &optionSet{m: map[string]*spec{}, seq: map[string]int{}}
}

func (o *optionSet) add(s spec) {
	if ex, ok := o.m[s.key]; ok && ex.hasValue == s.hasValue && ex.value == s.value {
		ex.ignoreErrors = ex.ignoreErrors || s.ignoreErrors
		return
	}
	c := s
	o.m[s.key] = &c
	o.n++
	o.seq[s.key] = o.n
}

func (o *optionSet) has(key string) bool { _, ok := o.m[key]; return ok }

func (o *optionSet) ordered() []*spec {
	out := make([]*spec, 0, len(o.m))
	for _, s := range o.m {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return o.seq[out[i].key] < o.seq[out[j].key] })
	return out
}

// escapeGlob quotes the glob metacharacters in a literal path prefix
// (procSysRoot may be a --root fixture with any name).
func escapeGlob(p string) string {
	var b strings.Builder
	for _, r := range p {
		if strings.ContainsRune(globChars+`\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// applyGlob expands a glob key against procSysRoot and writes every
// match that has no entry of its own in set (explicit assignment or
// `-key` exclusion), as systemd's apply_glob_option() does.
func applyGlob(set *optionSet, s *spec, strict, verbose bool, res *applyResult) {
	pattern := escapeGlob(procSysRoot) + "/" + s.key
	matches, err := filepath.Glob(pattern)
	if err != nil {
		if s.ignoreErrors {
			res.ignored++
			if verbose {
				fmt.Fprintf(os.Stderr, "ignored: bad glob %q: %v\n", s.rawKey, err)
			}
			return
		}
		res.errors = append(res.errors, fmt.Errorf("%s (from %s:%d): bad glob: %w",
			s.rawKey, s.source, s.sourceLineNo, err))
		return
	}
	if len(matches) == 0 {
		// systemd globs with GLOB_NOCHECK, so no match means writing
		// the pattern itself — ENOENT, judged like a missing key.
		path := filepath.Join(procSysRoot, s.key)
		record(classify(&fs.PathError{Op: "glob", Path: path, Err: syscall.ENOENT},
			*s, path, strict), verbose, res)
		return
	}
	prefix := filepath.Clean(procSysRoot) + "/"
	for _, m := range matches {
		key := strings.TrimPrefix(m, prefix)
		if set.has(key) {
			if verbose {
				fmt.Fprintf(os.Stderr, "not setting %s from %s (explicit setting exists)\n", key, s.rawKey)
			}
			continue
		}
		path, err := writeKey(key, s.value)
		if err != nil {
			err = classify(err, *s, path, strict)
		}
		record(err, verbose, res)
	}
}

// record folds one write outcome into res.
func record(err error, verbose bool, res *applyResult) {
	if err == nil {
		res.applied++
		return
	}
	var ig errIgnored
	if errors.As(err, &ig) {
		res.ignored++
		if ig.warn {
			res.warnings = append(res.warnings, ig.Error())
		} else if verbose {
			fmt.Fprintln(os.Stderr, err)
		}
		return
	}
	res.errors = append(res.errors, err)
}

// applyFiles reads every path into one option set (later files
// override earlier ones — discover() already orders them late-wins),
// then applies the set. A malformed line is an error that is
// reported and skipped; the rest of its file still applies.
func applyFiles(paths []string, strict, verbose bool) *applyResult {
	res := &applyResult{}
	set := newOptionSet()
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			res.errors = append(res.errors, fmt.Errorf("open %s: %w", p, err))
			continue
		}
		specs, errs := parseFile(f, p)
		f.Close()
		res.errors = append(res.errors, errs...)
		for _, s := range specs {
			set.add(s)
		}
	}
	for _, s := range set.ordered() {
		if !s.hasValue {
			continue // exclusion only
		}
		if isGlob(s.key) {
			applyGlob(set, s, strict, verbose, res)
			continue
		}
		record(applySpec(*s, strict), verbose, res)
	}
	return res
}
