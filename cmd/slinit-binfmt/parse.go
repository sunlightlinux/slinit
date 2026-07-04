package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// spec is one binfmt_misc registration entry — an opaque line as far
// as the kernel is concerned, plus the name we peeled off it for
// bookkeeping (unregister-then-register, `--unregister` mode, etc.).
type spec struct {
	name string // 2nd field, used to name /proc/sys/fs/binfmt_misc/<name>
	line string // exact bytes written to /proc/sys/fs/binfmt_misc/register
}

// parseFile drops blank lines and comment lines (leading '#' or ';'
// per the systemd-binfmt(1) convention) and returns one spec per
// remaining line. Malformed lines error hard so an operator gets a
// specific fix-up target rather than silently missing a format.
func parseFile(r io.Reader, source string) ([]spec, error) {
	var out []spec
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		trimmed := strings.TrimLeft(raw, " \t")
		if trimmed == "" || trimmed[0] == '#' || trimmed[0] == ';' {
			continue
		}
		name, err := parseName(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", source, lineNo, err)
		}
		out = append(out, spec{name: name, line: trimmed})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseName pulls the 2nd delimiter-separated field out of a binfmt
// spec. The first character of the line is the delimiter; the format
// is `:name:type:offset:magic:mask:interpreter:flags` when the
// delimiter is `:`, but any single-byte non-alnum char is allowed.
func parseName(line string) (string, error) {
	if len(line) < 3 {
		return "", fmt.Errorf("spec too short to contain a delimiter and name")
	}
	delim := line[0]
	// Kernel forbids alnum and NUL as delimiter; systemd is stricter
	// and requires it to be printable non-alnum. We only reject the
	// definitely-bad cases so unusual but legal specs still work.
	if delim == 0 || (delim >= '0' && delim <= '9') ||
		(delim >= 'a' && delim <= 'z') ||
		(delim >= 'A' && delim <= 'Z') {
		return "", fmt.Errorf("delimiter %q is alnum/NUL", delim)
	}
	// Find the second occurrence of delim — everything between it and
	// the first is the name.
	i := strings.IndexByte(line[1:], delim)
	if i < 0 {
		return "", fmt.Errorf("no closing delimiter after name")
	}
	name := line[1 : 1+i]
	if name == "" {
		return "", fmt.Errorf("empty name field")
	}
	// The kernel accepts up to 128-ish characters and rejects '/' /
	// NUL. Both would produce clearly broken procfs paths.
	if strings.ContainsAny(name, "/\x00") {
		return "", fmt.Errorf("name %q contains illegal characters", name)
	}
	return name, nil
}
