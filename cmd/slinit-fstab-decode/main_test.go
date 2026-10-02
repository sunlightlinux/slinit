package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestDecode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"nothing to do", "/home", "/home"},
		{"empty", "", ""},

		// The five entries in sysvinit's table, which is the whole table.
		{"space", `/mnt/my\040disk`, "/mnt/my disk"},
		{"tab", `a\011b`, "a\tb"},
		{"newline", `a\012b`, "a\nb"},
		{"backslash by octal", `back\134slash`, `back\slash`},
		{"backslash by doubling", `two\\one`, `two\one`},

		{"several in one field", `/a\040b\011c\012d`, "/a b\tc\nd"},

		// NOT general octal, and this is the point: mount tables only
		// ever escape those five, and decoding more would corrupt a path
		// that genuinely contains a backslash followed by digits.
		{"other octal is left alone", `keep\101`, `keep\101`},
		{"zero is left alone", `keep\000`, `keep\000`},
		{"short octal is left alone", `keep\04`, `keep\04`},

		// An unrecognised escape keeps its backslash and the next
		// character is handled as an ordinary one.
		{"unknown escape", `a\xb`, `a\xb`},
		{"trailing lone backslash", `a\`, `a\`},
		{"only a backslash", `\`, `\`},

		// A field that is already decoded must survive unchanged, since
		// awk output may or may not have been escaped.
		{"literal space survives", "/mnt/my disk", "/mnt/my disk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decode(tc.in); got != tc.want {
				t.Errorf("decode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// decode can only shrink or preserve: every table entry replaces two or
// four bytes with one, and everything else is copied. A result longer
// than its input would mean the index arithmetic had gone wrong.
func TestDecodeNeverGrows(t *testing.T) {
	for _, in := range []string{
		`\040`, `\\\\`, `\x\y\z`, `\`, `\\`, `a\011\012\040\134b`,
	} {
		if got := decode(in); len(got) > len(in) {
			t.Errorf("decode(%q) = %q — longer than its input", in, got)
		}
	}
}

type execCall struct {
	path string
	argv []string
}

func captureExec(t *testing.T, lookErr error) *execCall {
	t.Helper()
	var seen execCall
	oldLook, oldExec := lookPathFunc, execFunc
	lookPathFunc = func(file string) (string, error) {
		if lookErr != nil {
			return "", &exec.Error{Name: file, Err: lookErr}
		}
		return "/usr/bin/" + file, nil
	}
	execFunc = func(path string, argv []string, _ []string) error {
		seen = execCall{path, argv}
		return nil
	}
	t.Cleanup(func() { lookPathFunc, execFunc = oldLook, oldExec })
	return &seen
}

// Only the ARGUMENTS are decoded. Upstream decodes argv[2..] and leaves
// the command alone, and that is right: the command is a path to look up,
// not a field out of a mount table.
func TestCommandIsNotDecodedButArgumentsAre(t *testing.T) {
	seen := captureExec(t, nil)

	rc := run([]string{`umount\040x`, `/mnt/my\040disk`, `/home`}, os.Stderr)
	if rc != 0 {
		t.Fatalf("exit = %d, want 0", rc)
	}

	// argv[0] is the command exactly as written, escapes and all.
	if seen.argv[0] != `umount\040x` {
		t.Errorf("argv[0] = %q — the command must not be decoded", seen.argv[0])
	}
	if seen.path != `/usr/bin/umount\040x` {
		t.Errorf("looked up %q, want the command as written", seen.path)
	}
	if seen.argv[1] != "/mnt/my disk" {
		t.Errorf("argv[1] = %q, want %q", seen.argv[1], "/mnt/my disk")
	}
	if seen.argv[2] != "/home" {
		t.Errorf("argv[2] = %q, want %q", seen.argv[2], "/home")
	}
	if len(seen.argv) != 3 {
		t.Errorf("argv = %q, want three elements", seen.argv)
	}
}

func TestUsageWithoutACommand(t *testing.T) {
	captureExec(t, nil)
	if rc := run(nil, os.Stderr); rc != exitUsage {
		t.Errorf("exit = %d, want %d", rc, exitUsage)
	}
}

// 127 is the documented status for "the command could not be run", and a
// caller distinguishes it from the command's own failure.
func TestExit127WhenCommandCannotBeFound(t *testing.T) {
	captureExec(t, exec.ErrNotFound)
	if rc := run([]string{"definitely-not-a-command"}, os.Stderr); rc != exitCannotExec {
		t.Errorf("exit = %d, want %d", rc, exitCannotExec)
	}
}

func TestExit127WhenExecItselfFails(t *testing.T) {
	oldLook, oldExec := lookPathFunc, execFunc
	lookPathFunc = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	execFunc = func(string, []string, []string) error { return syscall.ENOEXEC }
	t.Cleanup(func() { lookPathFunc, execFunc = oldLook, oldExec })

	if rc := run([]string{"notabinary"}, os.Stderr); rc != exitCannotExec {
		t.Errorf("exit = %d, want %d", rc, exitCannotExec)
	}
}

// A command with no arguments is the degenerate case and must still exec.
func TestCommandWithNoArguments(t *testing.T) {
	seen := captureExec(t, nil)
	if rc := run([]string{"true"}, os.Stderr); rc != 0 {
		t.Fatalf("exit = %d, want 0", rc)
	}
	if fmt.Sprint(seen.argv) != "[true]" {
		t.Errorf("argv = %q, want [true]", seen.argv)
	}
}
