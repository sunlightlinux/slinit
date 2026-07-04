package main

import (
	"fmt"
	"strings"
)

type options struct {
	files   []string
	strict  bool
	verbose bool
	root    string
}

// parseArgs is the same getopt_long-lite walker as slinit-binfmt.
func parseArgs(args []string) (options, error) {
	var opts options
	i := 0
	need := func(name, attached string) (string, error) {
		if attached != "" {
			return attached, nil
		}
		if i+1 >= len(args) {
			return "", fmt.Errorf("flag %s requires an argument", name)
		}
		i++
		return args[i], nil
	}
	for i = 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			opts.files = append(opts.files, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			opts.files = append(opts.files, a)
			continue
		}
		name := a
		attached := ""
		if strings.HasPrefix(name, "--") {
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				attached = name[eq+1:]
				name = name[:eq]
			}
		} else if len(name) > 2 {
			attached = name[2:]
			name = name[:2]
		}
		switch name {
		case "-s", "--strict":
			opts.strict = true
		case "-v", "--verbose":
			opts.verbose = true
		case "--root":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.root = v
		case "-h", "--help":
			printUsage()
			return opts, errHelp
		case "-V", "--version":
			return opts, errVersion
		default:
			return opts, fmt.Errorf("unknown flag: %s", name)
		}
	}
	return opts, nil
}

var (
	errHelp    = fmt.Errorf("help requested")
	errVersion = fmt.Errorf("version requested")
)

func printUsage() {
	fmt.Print(`Usage: slinit-sysctl [OPTIONS] [FILE...]

Without arguments, applies every *.conf under:
  /usr/lib/sysctl.d, /usr/local/lib/sysctl.d, /run/sysctl.d, /etc/sysctl.d
Then applies /etc/sysctl.conf if it exists.

Line format:
  key.dotted.or/slashed = value       # regular
  -key.dotted = value                 # ignore write errors (best-effort)
  ; or # in column 0                  # comment

Wildcards ('*'/'?') in keys are NOT supported and reject the line.

Options:
  -s, --strict     ignore the '-' prefix; every failure is an error
  -v, --verbose    print a summary line to stderr; log skipped writes
      --root DIR   prefix DIR onto every hardcoded path (test seam)
  -h, --help       this help
  -V, --version    version string

Exit: 0 ok  1 at least one non-ignored failure  2 usage
`)
}
