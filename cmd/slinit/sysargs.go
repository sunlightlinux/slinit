package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// parseArgs parses slinit's own command line into fs.
//
// As PID 1 the kernel hands init every boot parameter it did not
// recognise, so the command line holds words nobody wrote for slinit:
// sysvinit's "-b" (emergency) and "-a" (auto), "-z <junk>", "-h".
// With flag's ExitOnError any of them exited init, which the kernel
// answers with "Attempted to kill init!" — a panic. As dinit does
// while it processes system arguments, an option slinit does not
// know is ignored there, and so is a known one missing its value;
// a value that fails to parse is reported and the rest of the line
// is skipped. Outside PID 1 the caller wrote the line, and a mistake
// in it still stops slinit.
func parseArgs(fs *flag.FlagSet, args []string, pid1 bool) {
	if !pid1 {
		fs.Parse(args)
		return
	}
	fs.Init(fs.Name(), flag.ContinueOnError)
	fs.Usage = func() {}
	if err := fs.Parse(filterSysArgs(fs, args, os.Stderr)); err != nil {
		fmt.Fprintf(os.Stderr, "slinit: ignoring the rest of the command line\n")
	}
}

// filterSysArgs drops from args the options fs does not define and
// the defined ones given no value, reporting each to w. Positional
// words are moved after the options, behind a "--", rather than ending
// the scan: the kernel hands init every word it did not recognise, so a
// stray one (`splash`, LILO's `auto`, the value of sysvinit's `-z`)
// would otherwise hide every option after it from flag parsing, `-t`
// included, which slinit(8) says is always honoured. dinit scans the
// whole line the same way. Everything after a "--" stays positional.
func filterSysArgs(fs *flag.FlagSet, args []string, w io.Writer) []string {
	out := make([]string, 0, len(args)+1)
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		name := strings.TrimPrefix(a[1:], "-")
		name, _, hasValue := strings.Cut(name, "=")
		f := fs.Lookup(name)
		if f == nil {
			fmt.Fprintf(w, "slinit: ignoring unrecognized option: %s\n", a)
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); hasValue || (ok && bf.IsBoolFlag()) {
			out = append(out, a)
			continue
		}
		if i+1 >= len(args) {
			fmt.Fprintf(w, "slinit: ignoring option without its value: %s\n", a)
			continue
		}
		out = append(out, a, args[i+1])
		i++
	}
	if len(pos) > 0 {
		out = append(append(out, "--"), pos...)
	}
	return out
}
