package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// parseArgs parses slinit's own command line into fs and returns the
// positional words to take as boot service names.
//
// Outside PID 1 the caller wrote the line: it goes to flag parsing,
// a mistake in it still stops slinit, and every positional word is
// returned for the caller to check.
//
// As PID 1 the kernel hands init every boot parameter it did not
// recognise, so the command line holds words nobody wrote for slinit,
// and the line is scanned with dinit's rules instead (see
// scanSysArgs). Nothing on it may end init: an ExitOnError parse of
// sysvinit's "-b" or "-z" exited it, which the kernel answers with
// "Attempted to kill init!" — a panic.
func parseArgs(fs *flag.FlagSet, args []string, pid1 bool) []string {
	if !pid1 {
		fs.Parse(args)
		return fs.Args()
	}
	return scanSysArgs(fs, args, os.Stderr)
}

// sysArgsOff names the options after which, as in dinit, every naked
// word is a service name: -m and -o say the line was written for
// slinit, not by a bootloader. -s is not among them. In dinit it only
// marks the system init; in slinit it is system mode, which PID 1 is in
// anyway, and sysvinit's single-user -s reaches init from bootloader
// entries, so it must not turn `splash` or LILO's `auto` into services.
var sysArgsOff = map[string]bool{"m": true, "system-mgr": true, "o": true, "container": true}

// scanSysArgs applies the PID-1 command line to fs the way dinit's
// process_commandline_arg loop does, and returns the words to start.
//
//   - Every word beginning with '-' is an option, wherever it is: a
//     naked word does not end option parsing, so `splash -t rescue`
//     honours -t. A "--" is no exception (the kernel keeps the first
//     one for itself; dinit treats a later one as an unknown option).
//   - A naked word is a service name only while system arguments are
//     not being processed, which they are from the start; -m or -o
//     (true) stops processing them for the words AFTER it, not before.
//     Until then only "single" is taken.
//   - An option takes the next word as its value, whatever it looks
//     like, as dinit and flag do. slinit's -name=value form, and
//     long options with one dash, keep working.
//
// Where dinit errors out — an unknown option once system arguments are
// no longer processed, an option without its value, an empty word, a
// value that does not parse, an invalid service name — PID 1 must not
// refuse to boot, so the item is reported to w and ignored, and the
// scan goes on.
func scanSysArgs(fs *flag.FlagSet, args []string, w io.Writer) []string {
	processSysArgs := true
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" {
			fmt.Fprintf(w, "slinit: ignoring empty command-line argument\n")
			continue
		}
		if a[0] != '-' {
			if processSysArgs && a != "single" {
				continue
			}
			// dinit's validate_service_name.
			if a[0] == '.' || a[0] == '@' {
				fmt.Fprintf(w, "slinit: ignoring invalid service name: %s\n", a)
				continue
			}
			words = append(words, a)
			continue
		}
		name := strings.TrimPrefix(a[1:], "-")
		name, value, hasValue := strings.Cut(name, "=")
		var f *flag.Flag
		if name != "" && name[0] != '-' {
			f = fs.Lookup(name)
		}
		if f == nil {
			fmt.Fprintf(w, "slinit: ignoring unrecognized option: %s\n", a)
			continue
		}
		bf, isBool := f.Value.(interface{ IsBoolFlag() bool })
		isBool = isBool && bf.IsBoolFlag()
		if !hasValue {
			if isBool {
				value = "true"
			} else {
				if i+1 >= len(args) || args[i+1] == "" {
					fmt.Fprintf(w, "slinit: ignoring option without its value: %s\n", a)
					i++
					continue
				}
				i++
				value = args[i]
			}
		} else if value == "" && !isBool {
			fmt.Fprintf(w, "slinit: ignoring option without its value: %s\n", a)
			continue
		}
		// flag's numeric values store their zero before reporting the
		// error (-F abc would leave ready-fd at 0, stdin), so put back
		// what was there. Only the repeatable -t list would not survive
		// a Set of its String(), and it never fails.
		prev := f.Value.String()
		if err := fs.Set(name, value); err != nil {
			f.Value.Set(prev)
			fmt.Fprintf(w, "slinit: ignoring %s %q: %v\n", a, value, err)
			continue
		}
		if sysArgsOff[name] {
			if on, _ := strconv.ParseBool(value); on {
				processSysArgs = false
			}
		}
	}
	return words
}
