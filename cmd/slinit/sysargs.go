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
// the defined ones given no value, reporting each to w. It stops at
// the first word flag parsing would stop at, so positional arguments
// are passed through untouched.
func filterSysArgs(fs *flag.FlagSet, args []string, w io.Writer) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || len(a) < 2 || a[0] != '-' {
			return append(out, args[i:]...)
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
	return out
}
