package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// binfmtDirs lists the directories systemd-binfmt(1) scans, ordered
// least- to most-authoritative. When two files share a basename, the
// last one wins — which lets `/etc/binfmt.d/foo.conf` override the
// distro-shipped copy under `/usr/lib/binfmt.d/foo.conf`.
var binfmtDirs = []string{
	"/usr/lib/binfmt.d",
	"/usr/local/lib/binfmt.d",
	"/run/binfmt.d",
	"/etc/binfmt.d",
}

// registerPath is the kernel entry point where every accepted spec
// line is written. Kept as a var so tests can point at a scratch
// file without touching /proc.
var registerPath = "/proc/sys/fs/binfmt_misc/register"

// binfmtStatusDir is where the kernel exposes a file per registered
// format; writing "-1" to one of them unregisters it.
var binfmtStatusDir = "/proc/sys/fs/binfmt_misc"

// discover walks binfmtDirs (or the caller-provided override) and
// returns the effective *.conf files in path order. Later-directory
// entries with the same basename replace earlier ones.
func discover(dirs []string) []string {
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
	// Deterministic order so operators get consistent registration
	// logs even when the directory read order shifts.
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, seen[n])
	}
	return out
}
