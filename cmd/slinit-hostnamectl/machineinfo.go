// machineinfo.go — read/write /etc/machine-info in systemd(5) format.
//
// The file is os-release(5)-style: KEY=value or KEY="value" per line,
// blank lines and `#` comments allowed. Values with whitespace, `$`,
// backslashes, or quotes require double-quoting; simple values can
// stay bare. See machine-info(5) for the full grammar.
//
// The parser is line-oriented and preserves ordering + comments across
// round-trips so operator-authored files aren't reformatted when
// hostnamectl touches only one key.

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// machineInfoPath is the canonical location; overridable in tests.
var machineInfoPath = "/etc/machine-info"

// machineInfo holds the parsed representation of the file. Lines
// preserve their original text; keys is a lookup index by canonical
// name pointing back into lines[].
type machineInfo struct {
	lines []miLine
}

type miLine struct {
	raw   string // original text (blank, comment, or KEY=VALUE)
	key   string // upper-case KEY when this is an assignment, else ""
	value string // decoded value (quotes stripped, escapes resolved)
}

// loadMachineInfo reads the file; missing file yields an empty struct
// so the setter path can freely create it.
func loadMachineInfo() (*machineInfo, error) {
	f, err := os.Open(machineInfoPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &machineInfo{}, nil
		}
		return nil, err
	}
	defer f.Close()

	mi := &machineInfo{}
	s := bufio.NewScanner(f)
	// os-release lines are typically short but be generous.
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for s.Scan() {
		raw := s.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			mi.lines = append(mi.lines, miLine{raw: raw})
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq <= 0 {
			mi.lines = append(mi.lines, miLine{raw: raw})
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(trimmed[:eq]))
		val := decodeValue(strings.TrimSpace(trimmed[eq+1:]))
		mi.lines = append(mi.lines, miLine{raw: raw, key: key, value: val})
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return mi, nil
}

// get returns the decoded value for KEY (last-wins if repeated).
func (mi *machineInfo) get(key string) string {
	key = strings.ToUpper(key)
	var v string
	for _, l := range mi.lines {
		if l.key == key {
			v = l.value
		}
	}
	return v
}

// set rewrites the first occurrence of KEY in-place; missing keys are
// appended. Passing an empty value deletes the key (matches systemd's
// "set to empty string clears" semantics).
func (mi *machineInfo) set(key, value string) {
	key = strings.ToUpper(key)
	if value == "" {
		mi.delete(key)
		return
	}
	line := fmt.Sprintf("%s=%s", key, encodeValue(value))
	for i, l := range mi.lines {
		if l.key == key {
			mi.lines[i] = miLine{raw: line, key: key, value: value}
			// Drop later duplicates so a single key = single line.
			mi.lines = removeDupes(mi.lines, key, i)
			return
		}
	}
	mi.lines = append(mi.lines, miLine{raw: line, key: key, value: value})
}

// delete removes every assignment for KEY (blank/comment lines are
// kept in place).
func (mi *machineInfo) delete(key string) {
	key = strings.ToUpper(key)
	out := mi.lines[:0]
	for _, l := range mi.lines {
		if l.key == key {
			continue
		}
		out = append(out, l)
	}
	mi.lines = out
}

// save writes the file back atomically (tmp + rename), creating parent
// dirs if needed. Mode 0644 matches systemd's tmpfiles-installed default.
func (mi *machineInfo) save() error {
	dir := "/etc"
	if idx := strings.LastIndex(machineInfoPath, "/"); idx > 0 {
		dir = machineInfoPath[:idx]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".machine-info.")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	w := bufio.NewWriter(tmp)
	for _, l := range mi.lines {
		if _, err := fmt.Fprintln(w, l.raw); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), machineInfoPath)
}

// decodeValue strips os-release(5) quoting: double-quoted values
// interpret \" \\ \$ \` as literals; single-quoted values are literal;
// bare values pass through. Whitespace outside quotes is discarded.
func decodeValue(raw string) string {
	if raw == "" {
		return ""
	}
	if raw[0] == '"' {
		end := strings.LastIndexByte(raw, '"')
		if end <= 0 {
			return raw
		}
		return unescapeDouble(raw[1:end])
	}
	if raw[0] == '\'' {
		end := strings.LastIndexByte(raw, '\'')
		if end <= 0 {
			return raw
		}
		return raw[1:end]
	}
	// Bare token: cut at inline comment.
	if i := strings.IndexAny(raw, " \t#"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

func unescapeDouble(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			nx := s[i+1]
			switch nx {
			case '"', '\\', '$', '`':
				b.WriteByte(nx)
				i++
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// encodeValue quotes a value if needed, using the minimal form that
// round-trips through decodeValue. Empty values are the caller's
// problem (set() handles empty as delete).
func encodeValue(v string) string {
	if needsQuoting(v) {
		esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(v)
		return `"` + esc + `"`
	}
	return v
}

func needsQuoting(v string) bool {
	if v == "" {
		return true
	}
	for _, r := range v {
		switch {
		case r == ' ', r == '\t', r == '\n', r == '#':
			return true
		case r == '"', r == '\'', r == '\\', r == '$', r == '`':
			return true
		case r == '=', r == '&', r == '|', r == ';', r == '<', r == '>':
			return true
		case r == '(', r == ')', r == '{', r == '}':
			return true
		case r == '*', r == '?', r == '~':
			return true
		}
	}
	return false
}

func removeDupes(lines []miLine, key string, keep int) []miLine {
	out := lines[:0]
	for i, l := range lines {
		if i != keep && l.key == key {
			continue
		}
		out = append(out, l)
	}
	return out
}
