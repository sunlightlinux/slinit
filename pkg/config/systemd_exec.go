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

// execCommand is one command of an Exec*= line. Its words are in
// slinit's $ syntax (sdEnvWord): a $ that systemd leaves as text is
// written $$, and a variable systemd would substitute is left for
// slinit to expand.
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

// parsedExec is one command of an Exec*= line with its prefixes, and
// the variables systemd would substitute in it: refs those left for
// slinit to expand, bad those with no valid name, which systemd expands
// to nothing.
type parsedExec struct {
	execPrefixes
	cmd       execCommand
	refs, bad []string
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
		// systemd substitutes variables in the argv it runs, argv[0]
		// included, but not in the path it executes (exec_invoke): the
		// path keeps every $ as text.
		envWords := func(words []string) []string {
			out, refs, bad := systemdEnvArgv(words, pre.noEnvExpand)
			c.refs, c.bad = append(c.refs, refs...), append(c.bad, bad...)
			return out
		}
		switch {
		case pre.viaShell:
			// The shell runs the rest, its variables substituted word
			// by word and then joined with spaces, as one -c script;
			// with nothing to run, the shell itself. With @ its
			// argv[0] is the shell's path with a "-" in front, the
			// login-shell convention (exec_invoke in exec-invoke.c).
			c.cmd.argv = []string{path}
			if words := envWords(args); len(words) > 0 {
				c.cmd.argv = append(c.cmd.argv, "-c", strings.Join(words, " "))
			}
			if pre.separateArgv0 {
				c.cmd.argv0, c.cmd.hasArgv0 = "-"+path, true
			}
		case len(args) == 0:
			return refuse("empty executable name or zeroeth argument")
		case pre.separateArgv0:
			c.cmd.argv = append([]string{literalDollars(path)}, envWords(args[1:])...)
			// A $VAR argv[0] systemd splits, and the words after the
			// first become arguments; slinit gives command-argv0 the
			// whole value. One that comes to nothing leaves argv[0]
			// empty here.
			c.cmd.hasArgv0 = true
			if w := envWords(args[:1]); len(w) > 0 {
				c.cmd.argv0 = w[0]
			}
		default:
			c.cmd.argv = append([]string{literalDollars(path)}, envWords(args[1:])...)
		}
		cmds = append(cmds, c)
		if !semicolon {
			return cmds, warns, nil
		}
	}
}

// slinitCommandValue writes argv, its words in slinit's $ syntax
// (execCommand), as the value of a slinit command setting (command,
// stop-command, pre-start-command, ...) that the parser splits back into
// exactly argv, its variables expanded. A word with a blank, a quote, a
// backslash or a control character, an empty word, and one holding a
// ${VAR}, is written in double quotes, with " and \ escaped by a
// backslash, which slinit's parser removes inside quotes as outside: a
// ${VAR} is one argument in systemd, whatever its value, and one that is
// empty or unset an empty argument. A word that is a lone $VAR stays
// unquoted, so that slinit splits its value into words as systemd does.
// A newline cannot be written in a value of one line; ok is false if a
// word holds one.
func slinitCommandValue(argv []string) (value string, ok bool) {
	parts := make([]string, len(argv))
	for i, w := range argv {
		if strings.ContainsRune(w, '\n') {
			return "", false
		}
		if w != "" && !strings.Contains(w, "${") && !strings.ContainsFunc(w, func(r rune) bool {
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

// slinitArgv0Value writes argv0, the argv[0] an "@" gives, as the value
// of command-argv0. That value is the rest of its line with the blanks
// at either end trimmed, and it has no quoting, so an argv[0] holding a
// control character (a newline would end the line and start a setting
// of its own) or starting or ending with a blank cannot be written, and
// neither can an empty one, which slinit reads as no command-argv0. ok
// is false then, with why. argv0 is in slinit's $ syntax already
// (execCommand), which command-argv0 expands as the command does.
func slinitArgv0Value(argv0 string) (value, why string, ok bool) {
	switch {
	case argv0 == "":
		return "", "it is empty, which command-argv0 cannot express", false
	case strings.ContainsFunc(argv0, unicode.IsControl):
		return "", "it holds a control character, which a command-argv0 value cannot", false
	case strings.TrimSpace(argv0) != argv0:
		return "", "it starts or ends with a blank, which slinit trims from a command-argv0 value", false
	}
	return argv0, "", true
}

// literalDollars writes w in slinit's $ syntax with no variable: every
// $ doubled.
func literalDollars(w string) string { return strings.ReplaceAll(w, "$", "$$") }

// isEnvName is systemd's env_name_is_valid: ASCII letters, digits and
// _, not starting with a digit. slinit reads a $NAME the same way.
func isEnvName(s string) bool {
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isVarChar(s[i], false) {
			return false
		}
	}
	return true
}

// systemdEnvArgv writes argv, words of a systemd command, in slinit's $
// syntax, each by sdEnvWord, leaving out the words systemd erases. With
// literal (the ":" prefix) every $ is text.
func systemdEnvArgv(argv []string, literal bool) (out, refs, bad []string) {
	for _, w := range argv {
		if literal {
			out = append(out, literalDollars(w))
			continue
		}
		v, r, b, drop := sdEnvWord(w)
		refs, bad = append(refs, r...), append(bad, b...)
		if !drop {
			out = append(out, v)
		}
	}
	return out, refs, bad
}

// sdEnvWord writes one word of a systemd command in slinit's $ syntax,
// following replace_env_argv (src/basic/env-util.c), where systemd
// substitutes:
//
//   - a word that is "$NAME" and nothing else, by the variable's value
//     split into words (none if it is unset or empty);
//   - "${NAME}" anywhere in a word, by the value as it is;
//   - "$$" by "$".
//
// Every other $ is text: "$NAME" inside a word (the shell's "echo $HOME"
// in sh -c), "$1", "$@", "${A:-x}" (an unsupported form, left alone as a
// whole), an unclosed "${". slinit expands all of these when the file is
// loaded, so their $ are doubled. "${NAME}" and a whole-word "$NAME"
// are left for slinit, which expands them alike. A "${...}" whose
// content is no valid name expands to nothing in systemd, and a word
// that is "$" followed by no valid name ("$1", "$HOME/x", "$") is
// erased: so they are here (drop), with the name in bad.
func sdEnvWord(w string) (out string, refs, bad []string, drop bool) {
	if strings.IndexByte(w, '$') < 0 {
		return w, nil, nil, false
	}
	if w[0] == '$' && (len(w) == 1 || w[1] != '{' && w[1] != '$') {
		if isEnvName(w[1:]) {
			return w, []string{w}, nil, false
		}
		return "", nil, []string{w}, true
	}
	var b strings.Builder
	for i := 0; i < len(w); {
		if w[i] != '$' {
			b.WriteByte(w[i])
			i++
			continue
		}
		switch {
		case i+1 < len(w) && w[i+1] == '$':
			b.WriteString("$$")
			i += 2
			continue
		case i+1 < len(w) && w[i+1] == '{':
			// The name runs to the first "}". A ":" before it is
			// syntax systemd does not support here, and with no "}"
			// at all there is no name: either way the text up to the
			// ":" (or the end), a "$$" in it included, is kept as it
			// is, and systemd reads on after the ":".
			k := strings.IndexAny(w[i+2:], "}:")
			if k >= 0 && w[i+2+k] == '}' {
				name := w[i+2 : i+2+k]
				if isEnvName(name) {
					b.WriteString(w[i : i+3+k])
					refs = append(refs, w[i:i+3+k])
				} else {
					bad = append(bad, w[i:i+3+k])
				}
				i += 3 + k
				continue
			}
			end := len(w)
			if k >= 0 {
				end = i + 3 + k
			}
			b.WriteString(literalDollars(w[i:end]))
			i = end
			continue
		}
		b.WriteString("$$")
		i++
	}
	return b.String(), refs, bad, false
}
