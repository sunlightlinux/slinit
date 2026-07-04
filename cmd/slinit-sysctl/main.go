// slinit-sysctl — systemd-sysctl(1) clone.
//
// Applies kernel tunables from sysctl.d(5) config files to
// /proc/sys/*. Called once at boot so every /etc/sysctl.d/*.conf and
// /usr/lib/sysctl.d/*.conf entry (net.ipv4.ip_forward=1,
// vm.swappiness=60, fs.file-max=…, kernel.printk=4 4 1 7, and
// friends) actually takes effect. Without it the kernel boots with
// its default values regardless of what the distro shipped under
// /usr/lib/sysctl.d/.
//
// Usage
//
//	slinit-sysctl                # apply everything the discovery finds
//	slinit-sysctl PATH1 PATH2    # apply only the named files
//	slinit-sysctl --strict       # disable dash-prefix "ignore errors"
//	slinit-sysctl --root=DIR     # test seam: rewrite every hardcoded path
//
// Exit codes: 0 success  1 partial failure  2 usage.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	exitOK       = 0
	exitFailure  = 1
	exitBadUsage = 2
)

var version = "dev"

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		switch err {
		case errHelp:
			os.Exit(exitOK)
		case errVersion:
			fmt.Printf("slinit-sysctl %s\n", version)
			os.Exit(exitOK)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitBadUsage)
	}

	if opts.root != "" {
		sysctlDirs = prefixDirs(opts.root, sysctlDirs)
		legacySysctlConf = filepath.Join(opts.root, legacySysctlConf)
		procSysRoot = filepath.Join(opts.root, procSysRoot)
	}

	paths := opts.files
	if len(paths) == 0 {
		paths = discover(sysctlDirs, legacySysctlConf)
	}
	res := applyFiles(paths, opts.strict, opts.verbose)
	for _, e := range res.errors {
		fmt.Fprintln(os.Stderr, e)
	}
	if opts.verbose {
		fmt.Fprintln(os.Stderr, res.String())
	}
	if len(res.errors) > 0 {
		os.Exit(exitFailure)
	}
}

// prefixDirs joins root onto every entry, matching what
// slinit-binfmt does — used by the --root test seam so a fixture
// tree replaces the system paths without any conditional branches
// inside apply().
func prefixDirs(root string, dirs []string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = filepath.Join(root, strings.TrimPrefix(d, "/"))
	}
	return out
}
