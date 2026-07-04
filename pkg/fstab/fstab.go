// Package fstab parses /etc/fstab in the standard 6-column format
// (fs_spec, fs_file, fs_vfstype, fs_mntops, fs_freq, fs_passno).
//
// The parser is deliberately Linux-flavoured — no /etc/vfstab
// support, no zsh-style comments — because slinit only targets
// Linux. See fstab(5) for the field definitions.
package fstab

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// DefaultPath is the standard fstab location. Kept as a var so tests
// can point at a fixture without touching /etc.
var DefaultPath = "/etc/fstab"

// Entry is one non-blank, non-comment row from fstab.
type Entry struct {
	Spec    string // fs_spec — device or LABEL=/UUID= spec
	File    string // fs_file — mountpoint
	VFSType string // fs_vfstype — filesystem type
	MntOps  string // fs_mntops — comma-separated mount options
	Freq    int    // fs_freq — dump(8) frequency, usually 0
	PassNo  int    // fs_passno — fsck(8) pass, 0 = don't check
}

// Options returns the comma-separated mount options as a slice with
// whitespace trimmed; kept off Entry itself so the raw string can be
// re-emitted verbatim for tools that need it.
func (e Entry) Options() []string {
	if e.MntOps == "" {
		return nil
	}
	parts := strings.Split(e.MntOps, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Parse reads r line-by-line. Blank lines and lines starting with '#'
// (after whitespace) are ignored; anything else must have 4 to 6
// whitespace-separated fields. Missing freq/passno default to 0,
// matching mount(8)'s behaviour.
func Parse(r io.Reader) ([]Entry, error) {
	var entries []Entry
	sc := bufio.NewScanner(r)
	// The default 64KB scanner buffer is fine for a fstab — no line
	// realistically exceeds a few hundred bytes.
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		trimmed := strings.TrimLeft(raw, " \t")
		if trimmed == "" || trimmed[0] == '#' {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 4 || len(fields) > 6 {
			return nil, fmt.Errorf("fstab line %d: expected 4-6 fields, got %d",
				lineNo, len(fields))
		}
		e := Entry{
			Spec:    unescape(fields[0]),
			File:    unescape(fields[1]),
			VFSType: fields[2],
			MntOps:  fields[3],
		}
		if len(fields) >= 5 {
			n, err := strconv.Atoi(fields[4])
			if err != nil {
				return nil, fmt.Errorf("fstab line %d: freq: %w", lineNo, err)
			}
			e.Freq = n
		}
		if len(fields) == 6 {
			n, err := strconv.Atoi(fields[5])
			if err != nil {
				return nil, fmt.Errorf("fstab line %d: passno: %w", lineNo, err)
			}
			e.PassNo = n
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// ReadFile is a convenience wrapper around Parse for a filesystem
// path. Returns an error if the file cannot be opened.
func ReadFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// FindByFile returns the first entry whose mountpoint matches path,
// or nil if none does. Matches the C original's getmntfile() helper.
func FindByFile(entries []Entry, path string) *Entry {
	for i := range entries {
		if entries[i].File == path {
			return &entries[i]
		}
	}
	return nil
}

// unescape decodes fstab's classic octal-triplet escapes: \040 for a
// space in a mountpoint, \011 for tab, etc. util-linux and libmount
// use the same encoding when they emit fstab-like lines.
func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+3 >= len(s) {
			b.WriteByte(c)
			continue
		}
		// Look for three octal digits after '\'.
		d1, d2, d3 := s[i+1], s[i+2], s[i+3]
		if !isOctal(d1) || !isOctal(d2) || !isOctal(d3) {
			b.WriteByte(c)
			continue
		}
		v := int(d1-'0')*64 + int(d2-'0')*8 + int(d3-'0')
		b.WriteByte(byte(v))
		i += 3
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }
