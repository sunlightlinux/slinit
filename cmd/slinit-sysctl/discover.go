package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sysctlDirs mirrors systemd-sysctl(1). Least- to most-authoritative;
// same-basename collisions resolve to the later directory so
// /etc/sysctl.d/foo.conf overrides /usr/lib/sysctl.d/foo.conf.
var sysctlDirs = []string{
	"/usr/lib/sysctl.d",
	"/usr/local/lib/sysctl.d",
	"/run/sysctl.d",
	"/etc/sysctl.d",
}

// legacySysctlConf is the single-file spelling preserved for
// backwards compat with pre-.d systems. Applied last so any of its
// keys wins over /etc/sysctl.d/*.conf.
var legacySysctlConf = "/etc/sysctl.conf"

// procSysRoot names the kernel's sysctl surface. Kept as a var so
// tests can retarget a scratch tree.
var procSysRoot = "/proc/sys"

// discover walks dirs (in order) plus the legacy single file, and
// returns the effective *.conf paths. Alphabetical order within the
// dedup map so a run against the same tree emits the same sequence
// of log lines every time.
func discover(dirs []string, legacy string) []string {
	seen := map[string]string{} // basename → chosen path
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".conf") {
				continue
			}
			seen[name] = filepath.Join(d, name)
		}
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names)+1)
	for _, n := range names {
		out = append(out, seen[n])
	}
	// Legacy /etc/sysctl.conf is appended after the .d contents so
	// operators who kept the old one-file spelling get the final say.
	if legacy != "" {
		if _, err := os.Stat(legacy); err == nil {
			out = append(out, legacy)
		}
	}
	return out
}
