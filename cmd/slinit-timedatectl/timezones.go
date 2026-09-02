// timezones.go — enumerate + validate IANA timezones.
//
// Preferred source is /usr/share/zoneinfo/zone1970.tab (POSIX 2015
// replaced zone.tab with this superset); fall back to zone.tab, then
// to walking /usr/share/zoneinfo/ recursively as a last resort. The
// walk is scoped to files whose first four bytes are "TZif" — the
// tzfile(5) magic — so we do not surface pseudo-entries like
// leap-seconds.list or posixrules.

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Overridable in tests.
var (
	zoneinfoDir  = "/usr/share/zoneinfo"
	localtimeSym = "/etc/localtime"
	timezoneFile = "/etc/timezone"
)

// listTimezones returns the sorted, deduplicated list of IANA zone
// names available on the host. Empty result with err=nil means the
// zone directory exists but is genuinely empty (should never happen
// on a real host, but the tests rely on it being non-fatal).
func listTimezones() ([]string, error) {
	if zones, ok := readZoneTab(filepath.Join(zoneinfoDir, "zone1970.tab")); ok {
		return zones, nil
	}
	if zones, ok := readZoneTab(filepath.Join(zoneinfoDir, "zone.tab")); ok {
		return zones, nil
	}
	return walkZoneinfo(zoneinfoDir)
}

// readZoneTab parses zone1970.tab / zone.tab. Both files have the
// same field layout for the zone name (column 3), differing only in
// whether column 1 is a single country code (zone.tab) or a
// comma-separated list (zone1970.tab). Returns (nil, false) if the
// file cannot be opened.
func readZoneTab(path string) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	seen := map[string]struct{}{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		zone := fields[2]
		// Defense-in-depth: an entry whose zone name would let a
		// downstream filepath.Join escape the zoneinfo tree, or one
		// with an embedded NUL that would truncate at a filesystem
		// boundary, gets dropped at enumeration time. validateZone
		// catches this again at set-timezone time, but a corrupted
		// zone.tab shouldn't even surface such names to callers
		// listing timezones. Caught by FuzzReadZoneTab.
		if strings.HasPrefix(zone, "/") || strings.Contains(zone, "..") ||
			strings.ContainsRune(zone, '\x00') {
			continue
		}
		seen[zone] = struct{}{}
	}
	if s.Err() != nil {
		return nil, false
	}
	out := make([]string, 0, len(seen))
	for z := range seen {
		out = append(out, z)
	}
	sort.Strings(out)
	return out, true
}

// walkZoneinfo is the fallback path for hosts without a zone*.tab
// index. Recognizes real tzfile(5) archives by their magic and
// strips the leading directory prefix to produce IANA-style names
// (e.g. Europe/Bucharest).
func walkZoneinfo(root string) ([]string, error) {
	var zones []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		if info.IsDir() {
			// Skip the "posix" and "right" mirrors — they duplicate
			// the top-level zones under alternate leap-second regimes.
			base := filepath.Base(path)
			if path != root && (base == "posix" || base == "right") {
				return filepath.SkipDir
			}
			return nil
		}
		if !isTZif(path) {
			return nil
		}
		name := strings.TrimPrefix(path, root+string(os.PathSeparator))
		zones = append(zones, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(zones)
	return zones, nil
}

// isTZif returns true when the first four bytes of the file are the
// tzfile(5) magic. Fast rejection for symlinks, docs, and metadata.
func isTZif(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [4]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return false
	}
	return bytes.Equal(head[:], []byte("TZif"))
}

// validateZone confirms that ZONE names a file under zoneinfoDir with
// the tzfile magic. Rejects path-escape attempts (../..) and absolute
// paths so a hostile caller cannot symlink /etc/localtime out of the
// zoneinfo tree.
func validateZone(name string) error {
	if name == "" {
		return errors.New("timezone is empty")
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return fmt.Errorf("invalid timezone %q", name)
	}
	full := filepath.Join(zoneinfoDir, name)
	rel, err := filepath.Rel(zoneinfoDir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("invalid timezone %q", name)
	}
	fi, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("unknown timezone %q", name)
	}
	if fi.IsDir() {
		return fmt.Errorf("timezone %q is a directory", name)
	}
	if !isTZif(full) {
		return fmt.Errorf("timezone %q is not a valid tzfile", name)
	}
	return nil
}

// currentZoneName returns the IANA zone name for the running system,
// derived from /etc/localtime's symlink target. Empty string when
// the symlink is missing or points outside the zoneinfo tree.
func currentZoneName() string {
	target, err := os.Readlink(localtimeSym)
	if err != nil {
		return ""
	}
	// The link may be absolute or relative to /etc.
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(localtimeSym), target)
	}
	target = filepath.Clean(target)
	prefix := filepath.Clean(zoneinfoDir) + string(os.PathSeparator)
	if !strings.HasPrefix(target, prefix) {
		return ""
	}
	return strings.TrimPrefix(target, prefix)
}
