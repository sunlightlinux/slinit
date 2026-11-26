package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// spec is one sysctl.d(5) line: either a `key = value` assignment or,
// when hasValue is false, a `-key` exclusion that keeps a glob
// assignment from touching that key. ignoreErrors is the leading `-`
// on an assignment (systemd-sysctl: "ignore failure").
type spec struct {
	key          string // slashed form, ready to append to procSysRoot
	rawKey       string // as written, for diagnostics
	value        string
	hasValue     bool // false: `-key` exclusion, nothing is written
	ignoreErrors bool
	source       string
	sourceLineNo int
}

// globChars are the characters that make a key a pattern, as in
// systemd's string_is_glob().
const globChars = "*?["

func isGlob(key string) bool { return strings.ContainsAny(key, globChars) }

// parseFile iterates r and returns one spec per assignment or
// exclusion line. Blank / comment lines are dropped silently. A
// malformed line is reported (as "source:line: reason") and skipped;
// the rest of the file is still returned, as systemd-sysctl does —
// one bad line must not throw away every other tunable in the file.
//
// Recognised comment prefixes: `#` and `;`, with any leading
// whitespace tolerated. Line continuation (`\` at EOL) is not
// supported.
func parseFile(r io.Reader, source string) ([]spec, []error) {
	var out []spec
	var errs []error
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed[0] == '#' || trimmed[0] == ';' {
			continue
		}
		s, err := parseLine(trimmed)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s:%d: %w, ignoring line", source, lineNo, err))
			continue
		}
		s.source = source
		s.sourceLineNo = lineNo
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", source, err))
	}
	return out, errs
}

// parseLine splits `KEY = VALUE`. A leading `-` on KEY flags
// best-effort mode; `-KEY` with no `=` is an exclusion for glob
// assignments. KEY may be a glob ('*', '?', '[').
func parseLine(line string) (spec, error) {
	var rawKey, value string
	hasValue := false
	ignore := false
	if eq := strings.IndexByte(line, '='); eq >= 0 {
		rawKey = strings.TrimSpace(line[:eq])
		value = strings.TrimSpace(line[eq+1:])
		hasValue = true
	} else {
		if !strings.HasPrefix(line, "-") {
			return spec{}, fmt.Errorf("line is not an assignment: %q", line)
		}
		rawKey = strings.TrimSpace(line)
	}
	if rawKey == "" {
		return spec{}, fmt.Errorf("empty key")
	}
	if rawKey[0] == '-' {
		ignore = hasValue
		rawKey = strings.TrimSpace(rawKey[1:])
		if rawKey == "" {
			return spec{}, fmt.Errorf("empty key after '-' prefix")
		}
	}
	// Normalise to slashes so we can Join onto /proc/sys directly, as
	// sysctl.d(5) does: if the first separator is '.', dots and slashes
	// swap (net.ipv4.conf.eth0/100.rp_filter names the VLAN eth0.100);
	// if it is '/', the key is used as is, dots included.
	slashed := rawKey
	if i := strings.IndexAny(rawKey, "./"); i >= 0 && rawKey[i] == '.' {
		slashed = strings.Map(func(r rune) rune {
			switch r {
			case '.':
				return '/'
			case '/':
				return '.'
			}
			return r
		}, rawKey)
	}
	if strings.HasPrefix(slashed, "/") {
		return spec{}, fmt.Errorf("key cannot start with '/': %q", rawKey)
	}
	if strings.Contains(slashed, "//") {
		return spec{}, fmt.Errorf("key cannot contain empty segments: %q", rawKey)
	}
	// Dots now survive in a slashed key, so "." and ".." must be
	// refused as elements: they would leave /proc/sys.
	for _, el := range strings.Split(slashed, "/") {
		if el == "." || el == ".." {
			return spec{}, fmt.Errorf("key cannot contain %q elements: %q", el, rawKey)
		}
	}
	return spec{
		key:          slashed,
		rawKey:       rawKey,
		value:        value,
		hasValue:     hasValue,
		ignoreErrors: ignore,
	}, nil
}
