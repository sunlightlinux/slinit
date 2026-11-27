package main

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzRenderSenderFields puts arbitrary bytes in every field a sender
// chooses (the message, the identifier, _COMM, the hostname, the unit)
// and renders the event in each terminal format. Any local user picks
// these ("IDENT:" on /dev/log, PR_SET_NAME, ...), and until 495086a only
// the message was escaped: an escape sequence reached the terminal of
// the admin reading the journal, and a newline in a verbose value forged
// a field line ("    _UID=0").
//
// Checked, whatever the input:
//   - no C0 control other than tab and newline, no DEL, no C1 control
//     and no invalid UTF-8 reaches the output;
//   - in the short formats every line after the first is indented (a
//     message continuation), so a newline cannot start what reads as
//     another entry;
//   - in verbose, every line indented exactly four spaces names a field
//     the same event shows when its sender fields are plain, so a value
//     cannot forge a field line.
//
// cat prints the bare message, newlines and all, as systemd's does; it
// is checked for control characters only.
func FuzzRenderSenderFields(f *testing.F) {
	f.Add("hello", "sudo", "sshd", "ceres", "sshd.service")
	f.Add("hi\n    _UID=0\x1b[2J", "sudo\x1b]0;owned\x07", "x\x1b[2J", "h\nost", "u\rnit")
	f.Add("\x9b31m red", "\xff\xfe", "\u0085next", "\t", "")
	f.Add("a\nNov 27 00:00:00 ceres root[1]: forged", "id\n", "c", "h", "u")
	f.Fuzz(func(t *testing.T, msg, ident, comm, host, unit string) {
		formats := []outputFormat{fmtShort, fmtShortPrecise, fmtShortISO, fmtShortISOPrecise,
			fmtShortFull, fmtShortMonotonic, fmtShortUnix, fmtWithUnit, fmtCat, fmtVerbose}

		plain := mkEvent()
		plain.Msg, plain.SyslogIdentifier, plain.Comm, plain.Hostname, plain.Unit =
			"m", "i", "c", "h", "u"
		var pb bytes.Buffer
		if err := render(&pb, fmtVerbose, plain, renderOpts{}); err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, l := range strings.Split(pb.String(), "\n") {
			if n, ok := verboseFieldName(l); ok {
				names[n] = true
			}
		}

		e := mkEvent()
		e.Msg, e.SyslogIdentifier, e.Comm, e.Hostname, e.Unit = msg, ident, comm, host, unit
		for _, fm := range formats {
			var buf bytes.Buffer
			if err := render(&buf, fm, e, renderOpts{}); err != nil {
				continue // a refused event prints nothing to check
			}
			out := buf.String()
			for i := 0; i < len(out); {
				r, size := utf8.DecodeRuneInString(out[i:])
				if (r < 0x20 && r != '\t' && r != '\n') || (r >= 0x7f && r <= 0x9f) ||
					(r == utf8.RuneError && size == 1) {
					t.Fatalf("format %s: %q reaches the terminal in %q", fm, out[i:i+size], out)
				}
				i += size
			}
			if fm == fmtCat {
				continue
			}
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			if fm == fmtVerbose {
				for _, l := range lines {
					if n, ok := verboseFieldName(l); ok && !names[n] {
						t.Fatalf("verbose: a sender's value forged the field line %q in %q", l, out)
					}
				}
				continue
			}
			for _, l := range lines[1:] {
				if l == "" || l[0] != ' ' {
					t.Fatalf("format %s: a sender's newline started an unindented line %q in %q", fm, l, out)
				}
			}
		}
	})
}

// verboseFieldName returns NAME for a verbose field line, "    NAME=value"
// with exactly four spaces of indent.
func verboseFieldName(l string) (string, bool) {
	if !strings.HasPrefix(l, "    ") || len(l) < 5 || l[4] == ' ' {
		return "", false
	}
	name, _, ok := strings.Cut(l[4:], "=")
	return name, ok
}
