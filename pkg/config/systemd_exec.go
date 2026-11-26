package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Exec*= lines of a systemd unit.
//
// systemd splits an Exec*= value into words itself, with its own
// quoting and C escapes (config_parse_exec and extract_first_word in
// src/core/load-fragment.c and src/basic/extract-word.c). The value
// used to be copied into the slinit file as written and split by
// slinit's command parser, whose rules differ: "" vanished instead of
// being an empty argument, \t and \n lost their backslash and became a
// plain t and n, \x41 became x41, and a lone ; (systemd's separator
// between commands) became an argument. The line is now split here, as
// systemd splits it, and each word is written back in the form slinit's
// parser turns into that same word.

// execCommand is one command of an Exec*= line.
type execCommand struct {
	argv     []string // what runs, path first
	argv0    string   // with @: argv[0] in place of the path
	hasArgv0 bool
}

var (
	errSDUnbalancedQuote = errors.New("unbalanced quoting")
	errSDBadEscape       = errors.New("unknown escape sequence")
)

// isSDSpace reports whether c is in systemd's WHITESPACE.
func isSDSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// extractSystemdWord is systemd's extract_first_word with
// EXTRACT_UNQUOTE|EXTRACT_CUNESCAPE: quotes anywhere in a word group,
// a backslash takes a C escape, and blanks separate. With relax, as on
// systemd's retry (EXTRACT_UNESCAPE_RELAX), an unknown escape and an
// unquoted trailing backslash are kept as written. found is false when
// s holds no further word.
func extractSystemdWord(s string, relax bool) (word, rest string, found bool, err error) {
	i := 0
	for i < len(s) && isSDSpace(s[i]) {
		i++
	}
	if i == len(s) {
		return "", "", false, nil
	}
	var b []byte
	var quote byte
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\':
			i++
			if i == len(s) {
				if relax && quote == 0 {
					return string(append(b, '\\')), "", true, nil
				}
				if quote != 0 {
					return "", "", false, errSDUnbalancedQuote
				}
				return "", "", false, errSDBadEscape
			}
			if out, n, ok := cunescapeOne(s[i:]); ok {
				b = append(b, out...)
				i += n
			} else if relax {
				b = append(b, '\\', s[i])
				i++
			} else {
				return "", "", false, errSDBadEscape
			}
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				b = append(b, c)
			}
			i++
		case c == '\'' || c == '"':
			quote = c
			i++
		case isSDSpace(c):
			for i < len(s) && isSDSpace(s[i]) {
				i++
			}
			return string(b), s[i:], true, nil
		default:
			b = append(b, c)
			i++
		}
	}
	if quote != 0 {
		return "", "", false, errSDUnbalancedQuote
	}
	return string(b), "", true, nil
}

// extractSystemdWordAndWarn is extract_first_word_and_warn: an unknown
// escape is retried relaxed and kept as written, with a warning.
func extractSystemdWordAndWarn(key, s string) (word, rest string, found bool, warns []SystemdWarning, err error) {
	word, rest, found, err = extractSystemdWord(s, false)
	if errors.Is(err, errSDBadEscape) {
		word, rest, found, err = extractSystemdWord(s, true)
		if err == nil {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
				"%s=: unknown escape sequence in %q kept as written, as systemd does", key, word)})
		}
	}
	if errors.Is(err, errSDBadEscape) {
		err = errSDUnbalancedQuote
	}
	return
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// cunescapeOne is systemd's cunescape_one (src/basic/escape.c) without
// NUL: s starts after the backslash; it returns the bytes the escape
// stands for and how many bytes of s it took.
func cunescapeOne(s string) (out []byte, n int, ok bool) {
	hexRun := func(k int) (uint32, bool) {
		if len(s) < 1+k {
			return 0, false
		}
		var v uint32
		for j := 1; j <= k; j++ {
			d := unhex(s[j])
			if d < 0 {
				return 0, false
			}
			v = v<<4 | uint32(d)
		}
		return v, true
	}
	switch c := s[0]; c {
	case 'a':
		return []byte{'\a'}, 1, true
	case 'b':
		return []byte{'\b'}, 1, true
	case 'f':
		return []byte{'\f'}, 1, true
	case 'n':
		return []byte{'\n'}, 1, true
	case 'r':
		return []byte{'\r'}, 1, true
	case 't':
		return []byte{'\t'}, 1, true
	case 'v':
		return []byte{'\v'}, 1, true
	case '\\', '"', '\'':
		return []byte{c}, 1, true
	case 's':
		return []byte{' '}, 1, true
	case 'x':
		v, ok := hexRun(2)
		if !ok || v == 0 {
			return nil, 0, false
		}
		return []byte{byte(v)}, 3, true
	case 'u', 'U':
		k := 4
		if c == 'U' {
			k = 8
		}
		v, ok := hexRun(k)
		if !ok || v == 0 {
			return nil, 0, false
		}
		if c == 'u' && v >= 0xD800 && v <= 0xDFFF {
			return nil, 0, false
		}
		if c == 'U' && (v >= 0x110000 || v&0xFFFFF800 == 0xD800 ||
			(v >= 0xFDD0 && v <= 0xFDEF) || v&0xFFFE == 0xFFFE) {
			return nil, 0, false
		}
		return utf8.AppendRune(nil, rune(v)), 1 + k, true
	case '0', '1', '2', '3', '4', '5', '6', '7':
		if len(s) < 3 {
			return nil, 0, false
		}
		var v int
		for j := 0; j < 3; j++ {
			if s[j] < '0' || s[j] > '7' {
				return nil, 0, false
			}
			v = v<<3 | int(s[j]-'0')
		}
		if v == 0 || v > 255 {
			return nil, 0, false
		}
		return []byte{byte(v)}, 3, true
	}
	return nil, 0, false
}

// execPrefixes are the flags a word's leading prefix characters set,
// as in config_parse_exec.
type execPrefixes struct {
	ignoreFailure, separateArgv0, noEnvExpand, viaShell, fullPriv, noSetuid bool
}

// cutExecPrefixes reads the prefix characters off the first word. Each
// counts once; '+' and '!' exclude each other, and a second '!' after
// one is the old "!!", which systemd now ignores.
func cutExecPrefixes(w string) (execPrefixes, string) {
	var p execPrefixes
	ambientHack := false
	i := 0
loop:
	for ; i < len(w); i++ {
		switch c := w[i]; {
		case c == '-' && !p.ignoreFailure:
			p.ignoreFailure = true
		case c == '@' && !p.separateArgv0:
			p.separateArgv0 = true
		case c == ':' && !p.noEnvExpand:
			p.noEnvExpand = true
		case c == '|' && !p.viaShell:
			p.viaShell = true
		case c == '+' && !p.fullPriv && !p.noSetuid && !ambientHack:
			p.fullPriv = true
		case c == '!' && !p.fullPriv && !p.noSetuid && !ambientHack:
			p.noSetuid = true
		case c == '!' && !p.fullPriv && !ambientHack:
			p.noSetuid, ambientHack = false, true
		default:
			break loop
		}
	}
	return p, w[i:]
}

// parsedExec is one command of an Exec*= line with its prefixes.
type parsedExec struct {
	execPrefixes
	cmd execCommand
}

// parseExecLine splits an Exec*= value into its commands the way
// config_parse_exec does: words as extractSystemdWord reads them, a
// lone ";" between commands, "\;" for a literal ";" argument, and the
// specifiers expanded in each word once it is unquoted. A line systemd
// would refuse makes the unit fail (fatal) unless its command has the
// "-" prefix, which makes systemd ignore the line instead.
func parseExecLine(cfg *SystemdConfig, key, val string) (cmds []parsedExec, warns []SystemdWarning, fatal error) {
	p := val
	for {
		semicolon := false
		first, rest, found, w, err := extractSystemdWordAndWarn(key, p)
		warns = append(warns, w...)
		if err != nil {
			// systemd logs it and ignores the rest of the line.
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s=%s: %v — line ignored, as systemd does", key, val, err)})
			return cmds, warns, nil
		}
		if !found {
			return cmds, warns, nil
		}
		p = rest
		if first == ";" {
			continue
		}
		pre, f := cutExecPrefixes(first)
		refuse := func(why string) ([]parsedExec, []SystemdWarning, error) {
			if pre.ignoreFailure {
				warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s=%s: %s — command ignored, as systemd does for a \"-\" command", key, val, why)})
				return cmds, warns, nil
			}
			return nil, warns, fmt.Errorf("%s=%s: %s", key, val, why)
		}

		var args []string
		var path string
		if pre.viaShell {
			path = "/bin/sh"
			if f != "" {
				args = append(args, f)
			}
		} else {
			var w []SystemdWarning
			path, w = expandSpecifiers(cfg, key, f)
			warns = append(warns, w...)
			if path == "" {
				return refuse("empty executable path")
			}
			if !pre.separateArgv0 {
				args = append(args, path)
			}
		}
		for p != "" {
			if p[0] == ';' && (len(p) == 1 || isSDSpace(p[1])) {
				p = strings.TrimLeft(p[1:], " \t\n\r")
				semicolon = true
				break
			}
			if strings.HasPrefix(p, `\;`) && (len(p) == 2 || isSDSpace(p[2])) {
				p = strings.TrimLeft(p[2:], " \t\n\r")
				args = append(args, ";")
				continue
			}
			word, rest, found, w, err := extractSystemdWordAndWarn(key, p)
			warns = append(warns, w...)
			if err != nil {
				return refuse(err.Error())
			}
			if !found {
				break
			}
			p = rest
			word, w = expandSpecifiers(cfg, key, word)
			warns = append(warns, w...)
			args = append(args, word)
		}

		c := parsedExec{execPrefixes: pre}
		switch {
		case pre.viaShell:
			// "sh" (or "-sh" with @) runs the rest, joined with
			// spaces, as one -c script; with nothing to run, the shell
			// itself (exec-invoke.c).
			c.cmd.argv = []string{path}
			if len(args) > 0 {
				c.cmd.argv = append(c.cmd.argv, "-c", strings.Join(args, " "))
			}
			if pre.separateArgv0 {
				c.cmd.argv0, c.cmd.hasArgv0 = "-sh", true
			}
		case len(args) == 0:
			return refuse("empty executable name or zeroeth argument")
		case pre.separateArgv0:
			c.cmd.argv = append([]string{path}, args[1:]...)
			c.cmd.argv0, c.cmd.hasArgv0 = args[0], true
		default:
			c.cmd.argv = args
		}
		cmds = append(cmds, c)
		if !semicolon {
			return cmds, warns, nil
		}
	}
}

// slinitCommandValue writes argv as the value of a slinit command
// setting (command, stop-command, pre-start-command, ...) that the
// parser splits back into exactly argv. A word with a blank, a quote, a
// backslash or a control character, and an empty word, is written in
// double quotes, with " and \ escaped by a backslash, which slinit's
// parser removes inside quotes as outside. With literal, every $ is
// doubled, so that no variable is expanded (systemd's ":" prefix);
// otherwise $VAR and ${VAR} are left for slinit to expand as before,
// and only "$/", which is a word-splitting variable in slinit but plain
// text in systemd, is escaped. A newline cannot be written in a value
// of one line; ok is false if a word holds one.
func slinitCommandValue(argv []string, literal bool) (value string, ok bool) {
	parts := make([]string, len(argv))
	for i, w := range argv {
		if strings.ContainsRune(w, '\n') {
			return "", false
		}
		w = escapeDollars(w, literal)
		if w != "" && !strings.ContainsFunc(w, func(r rune) bool {
			return r == '"' || r == '\'' || r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r)
		}) {
			parts[i] = w
			continue
		}
		var b strings.Builder
		b.WriteByte('"')
		for j := 0; j < len(w); j++ {
			if w[j] == '"' || w[j] == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(w[j])
		}
		b.WriteByte('"')
		parts[i] = b.String()
	}
	return strings.Join(parts, " "), true
}

// escapeDollars doubles the $ of w that slinit would expand but systemd
// would not: every one with literal, else only the one in "$/".
func escapeDollars(w string, literal bool) string {
	if strings.IndexByte(w, '$') < 0 {
		return w
	}
	if literal {
		return strings.ReplaceAll(w, "$", "$$")
	}
	var b strings.Builder
	for i := 0; i < len(w); i++ {
		if w[i] == '$' && i+1 < len(w) {
			switch w[i+1] {
			case '$':
				b.WriteString("$$")
				i++
				continue
			case '/':
				b.WriteString("$$")
				continue
			}
		}
		b.WriteByte(w[i])
	}
	return b.String()
}
