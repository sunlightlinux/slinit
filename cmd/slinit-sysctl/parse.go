package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// spec is one `key = value` assignment plus the ignoreErrors flag
// set by a leading `-` (matches systemd-sysctl's semantics: dash
// prefix means "apply best-effort, don't fail the pass if the key
// is missing or write is refused").
type spec struct {
	key          string // slashed form, ready to append to procSysRoot
	rawKey       string // dotted form, for diagnostics
	value        string
	ignoreErrors bool
	source       string
	sourceLineNo int
}

// parseFile iterates r and returns one spec per key=value line.
// Blank / comment lines are dropped silently; a malformed line
// errors hard so the operator has a specific fix-up target.
//
// Recognised comment prefixes: `#` and `;` (systemd), with any
// leading whitespace tolerated. Line continuation (`\` at EOL) is
// not supported — systemd doesn't accept it either.
func parseFile(r io.Reader, source string) ([]spec, error) {
	var out []spec
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
			return nil, fmt.Errorf("%s:%d: %w", source, lineNo, err)
		}
		s.source = source
		s.sourceLineNo = lineNo
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseLine splits `KEY = VALUE`. Leading `-` on KEY flags
// best-effort mode. Wildcards ('*'/'?') in KEY are rejected — v1
// does not expand them, and silently accepting them would apply
// nothing while looking like it did.
func parseLine(line string) (spec, error) {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return spec{}, fmt.Errorf("missing '=' in %q", line)
	}
	rawKey := strings.TrimSpace(line[:eq])
	value := strings.TrimSpace(line[eq+1:])
	if rawKey == "" {
		return spec{}, fmt.Errorf("empty key")
	}
	ignore := false
	if rawKey[0] == '-' {
		ignore = true
		rawKey = strings.TrimSpace(rawKey[1:])
		if rawKey == "" {
			return spec{}, fmt.Errorf("empty key after '-' prefix")
		}
	}
	if strings.ContainsAny(rawKey, "*?") {
		return spec{}, fmt.Errorf("wildcards not supported: %q", rawKey)
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
		ignoreErrors: ignore,
	}, nil
}
