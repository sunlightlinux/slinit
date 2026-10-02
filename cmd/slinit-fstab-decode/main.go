// slinit-fstab-decode — drop-in replacement for sysvinit's
// fstab-decode(8).
//
// Mount tables escape the characters that would otherwise split a field:
// a mount point containing a space appears in /etc/fstab and /proc/mounts
// as `/mnt/my\040disk`. Anything that reads those tables with awk and
// feeds the result to umount has to undo that first, which is what this
// does: decode the escapes in its arguments, then become the command.
//
//	awk '$3 == "nfs" { print $2 }' /etc/fstab | xargs slinit-fstab-decode umount
//
// slinit-fstabinfo(8) is the neighbour that *queries* fstab; this one
// only unescapes and execs, and is what an init.d script already calls.
//
// Four details come from sysvinit's fstab-decode.c rather than its
// manual, and a naive implementation gets each of them wrong:
//
//   - The escape table is five fixed entries, not general octal. `\040`
//     is a space; `\101` is left as `\101`, not decoded to `A`.
//   - Only the ARGUMENTS are decoded. The command itself is not.
//   - An unrecognised escape keeps its backslash.
//   - execvp replaces the process, so the exit status is the command's
//     with nothing relaying it. 127 is returned only when the command
//     could not be started at all.
//
// Usage:
//
//	slinit-fstab-decode COMMAND [ARGUMENT ...]
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Seams for the tests: the real thing replaces this process, which a
// test cannot survive.
var (
	lookPathFunc = exec.LookPath
	execFunc     = syscall.Exec
)

const (
	exitUsage      = 1   // upstream returns EXIT_FAILURE for a missing command
	exitCannotExec = 127 // upstream's value when the command will not start
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr *os.File) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "Usage: slinit-fstab-decode command [arguments]")
		return exitUsage
	}

	// argv[0] of the new program is the command as written, and only the
	// arguments after it are decoded — upstream decodes argv[2..] and
	// leaves argv[1] alone. A command name with a literal `\040` in it
	// is therefore passed through untouched, which is what you want: it
	// is a path to look up, not a field out of a mount table.
	command := args[0]
	argv := make([]string, len(args))
	argv[0] = command
	for i, a := range args[1:] {
		argv[i+1] = decode(a)
	}

	path, err := lookPathFunc(command)
	if err != nil {
		// Upstream prints strerror(errno) from execvp; LookPath's error
		// already names the command and the reason, so printing it bare
		// would say the command twice.
		fmt.Fprintf(stderr, "slinit-fstab-decode: %s: %v\n", command, unwrapExecErr(err))
		return exitCannotExec
	}

	// Become the command rather than running it as a child. That is what
	// execvp does, and it is not a detail: the caller's shell keeps one
	// pid to wait on and signal, the exit status needs no relaying, and
	// an xargs pipeline behaves the same as it does with sysvinit's
	// version. Running it as a child would be observably different.
	if err := execFunc(path, argv, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "slinit-fstab-decode: %s: %v\n", command, err)
		return exitCannotExec
	}
	// Only reached when execFunc is a test stub; a real exec does not
	// return.
	return 0
}

// fstabEscapes is sysvinit's table, verbatim and in full.
//
// It is a fixed list rather than an octal parser, and that is the
// behaviour to copy: `\040` decodes to a space because it is in the
// table, while `\101` stays `\101` because it is not. Mount tables only
// ever escape these characters, and decoding more would corrupt a path
// that legitimately contains a backslash followed by digits.
var fstabEscapes = []struct {
	from string
	to   byte
}{
	{`\`, '\\'}, // `\\` in the input — a backslash escaping a backslash
	{"011", '\t'},
	{"012", '\n'},
	{"040", ' '},
	{"134", '\\'},
}

// decode unescapes one fstab-encoded field.
func decode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		rest := s[i+1:]
		matched := false
		for _, e := range fstabEscapes {
			if strings.HasPrefix(rest, e.from) {
				b.WriteByte(e.to)
				i += 1 + len(e.from)
				matched = true
				break
			}
		}
		if !matched {
			// An escape the table does not know keeps its backslash, and
			// the character after it is handled on the next pass as an
			// ordinary one. `\x` stays `\x`; a trailing lone `\` stays.
			b.WriteByte('\\')
			i++
		}
	}
	return b.String()
}

// unwrapExecErr reduces exec.LookPath's wrapper to its cause, so the
// message reads like upstream's strerror output instead of repeating the
// command name.
func unwrapExecErr(err error) error {
	var e *exec.Error
	if errors.As(err, &e) {
		return e.Err
	}
	return err
}
