package main

import (
	"fmt"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/fstab"
)

var version = "dev"

// parseArgs is the same getopt_long-lite walker used by the other
// slinit standalone binaries: fused short values (`-p=1`), spaced
// long values (`--fstype ext4`), and `--flag=value`. --passno accepts
// a `=N`/`<N`/`>N` operator, otherwise it's a plain mountpoint.
func parseArgs(args []string) (options, error) {
	opts := options{
		mode:      outputFile,
		fstabPath: fstab.DefaultPath,
	}
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
		case "-M", "--mount":
			opts.mode = outputMount
		case "-R", "--remount":
			opts.mode = outputRemount
		case "-b", "--blockdevice":
			opts.mode = outputBlockDev
		case "-o", "--options":
			opts.mode = outputOptions
		case "-m", "--mountargs":
			opts.mode = outputMountArgs
		case "-p", "--passno":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			op, val, plain, err := parsePassNoArg(v)
			if err != nil {
				return opts, err
			}
			if op != 0 {
				opts.selectors = append(opts.selectors, selector{passnoOp: op, passnoValue: val})
			} else {
				// Plain form: print the passno of an explicit mountpoint.
				opts.selectors = append(opts.selectors, selector{file: plain})
				opts.mode = outputPassno
			}
		case "-t", "--fstype":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.selectors = append(opts.selectors, selector{fstypes: strings.Split(v, ",")})
		case "--file":
			// Non-standard but useful: override fstab path (test seam).
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.fstabPath = v
		case "-h", "--help":
			printUsage()
			return opts, errHelp
		case "-V", "--version":
			return opts, errVersion
		case "-q", "--quiet":
			// Preserved for compat; the actual quiet check keys off
			// EINFO_QUIET so scripts already exporting that don't
			// need to pass -q too.
		case "-v", "--verbose":
			// Cosmetic — no per-line diagnostics to gate on.
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
	fmt.Print(`Usage: slinit-fstabinfo [OPTIONS] [MOUNTPOINT...]

Output modes (default: mountpoint):
  -b, --blockdevice           print the block device / fs_spec field
  -o, --options               print the mount options field
  -m, --mountargs             print the args mount(8) would consume
  -p, --passno {=N|<N|>N}     filter by passno with an operator
  -p, --passno MOUNTPOINT     print passno for a specific mountpoint

Actions:
  -M, --mount                 invoke mount(8) for matching entries
  -R, --remount               invoke mount -o remount for matching entries

Selection (each -t / -p OP N adds its matches to one list):
  -t, --fstype TYPE[,TYPE]    entries with the given fs type(s)
      MOUNTPOINT...           keep only these from the list; if the list
                              is empty, select these mountpoints instead

Misc:
      --file PATH             use PATH instead of /etc/fstab (test seam)
  -h, --help                  this help
  -V, --version               version string

Exit: 0 = ok  1 = nothing selected / mountpoint not in fstab
      other = sum of mount(8) exit codes  2 = usage
`)
}
