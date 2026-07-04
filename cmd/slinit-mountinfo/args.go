package main

import (
	"fmt"
	"regexp"
	"strings"
)

var version = "dev"

// parseArgs is the same getopt_long-lite walker used by the other
// standalone slinit binaries. Regex flags compile eagerly so a bad
// regex fails at parse time, not deep inside the filter loop.
func parseArgs(args []string) (options, error) {
	opts := options{
		field:      fieldMountPoint,
		procMounts: "/proc/mounts",
		etcFstab:   "/etc/fstab",
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
	compileRegex := func(name, val string) (*regexp.Regexp, error) {
		re, err := regexp.Compile(val)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid regex %q: %w", name, val, err)
		}
		return re, nil
	}

	for i = 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			for _, mp := range args[i+1:] {
				opts.mountpoints = append(opts.mountpoints, realpath(mp))
			}
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			if !strings.HasPrefix(a, "/") {
				return opts, fmt.Errorf("%q is not a mount point (must start with /)", a)
			}
			opts.mountpoints = append(opts.mountpoints, realpath(a))
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
		case "-e", "--netdev":
			opts.netdev = netYes
		case "-E", "--nonetdev":
			opts.netdev = netNo

		case "-f", "--fstype-regex":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			if opts.fstypeRe, err = compileRegex(name, v); err != nil {
				return opts, err
			}
		case "-F", "--skip-fstype-regex":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			if opts.skipFstypeRe, err = compileRegex(name, v); err != nil {
				return opts, err
			}
		case "-n", "--node-regex":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			if opts.nodeRe, err = compileRegex(name, v); err != nil {
				return opts, err
			}
		case "-N", "--skip-node-regex":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			if opts.skipNodeRe, err = compileRegex(name, v); err != nil {
				return opts, err
			}
		case "-o", "--options-regex":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			if opts.optionsRe, err = compileRegex(name, v); err != nil {
				return opts, err
			}
		case "-O", "--skip-options-regex":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			if opts.skipOptionsRe, err = compileRegex(name, v); err != nil {
				return opts, err
			}
		case "-p", "--point-regex":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			if opts.pointRe, err = compileRegex(name, v); err != nil {
				return opts, err
			}
		case "-P", "--skip-point-regex":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			if opts.skipPointRe, err = compileRegex(name, v); err != nil {
				return opts, err
			}

		case "-i", "--options":
			opts.field = fieldOptions
		case "-s", "--fstype":
			opts.field = fieldFstype
		case "-t", "--node":
			opts.field = fieldNode

		// Non-standard test seams: swap the fixture in for the
		// system paths without touching /proc or /etc.
		case "--proc-mounts":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.procMounts = v
		case "--fstab":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.etcFstab = v

		case "-h", "--help":
			printUsage()
			return opts, errHelp
		case "-V", "--version":
			return opts, errVersion
		case "-q", "--quiet", "-v", "--verbose":
			// Kept for compat; quiet is EINFO_QUIET-driven, verbose has
			// no per-entry diagnostics to gate.
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
	fmt.Print(`Usage: slinit-mountinfo [OPTIONS] [MOUNTPOINT...]

Output selectors (default: mountpoint):
  -i, --options                  print the mount-options field
  -s, --fstype                   print the filesystem type
  -t, --node                     print the device / spec field

Regex filters (each POSIX extended, --skip- variants exclude):
  -f, --fstype-regex REGEX       keep matching filesystem types
  -F, --skip-fstype-regex REGEX  drop matching filesystem types
  -n, --node-regex REGEX         keep matching device / spec
  -N, --skip-node-regex REGEX    drop matching device / spec
  -o, --options-regex REGEX      keep matching options strings
  -O, --skip-options-regex REGEX drop matching options strings
  -p, --point-regex REGEX        keep matching mountpoints
  -P, --skip-point-regex REGEX   drop matching mountpoints

Netdev filters (consult /etc/fstab for _netdev flag):
  -e, --netdev                   only entries flagged _netdev in fstab
  -E, --nonetdev                 only entries known to fstab without _netdev

Test seams (non-standard):
      --proc-mounts PATH         alternative mount table
      --fstab PATH               alternative fstab (for netdev lookup)

Common:
  -h, --help                     this help
  -V, --version                  version string

Exit: 0 = at least one entry matched  1 = no match  2 = usage
`)
}
