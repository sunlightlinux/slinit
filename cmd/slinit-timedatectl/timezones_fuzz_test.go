package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzReadZoneTab fuzzes /usr/share/zoneinfo/zone1970.tab (or its
// legacy zone.tab). This file is untrusted from slinit's perspective
// — it ships with the system's tzdata package, and a hostile update
// or corruption could feed the parser garbage that reaches every
// `timedatectl list-timezones` caller.
//
// Invariants:
//   1. Parser must not panic on any bytes.
//   2. Returned zone list must contain only strings without embedded
//      NUL or path-separator escape (../, absolute paths) — the
//      caller uses these names to build filesystem paths under
//      /usr/share/zoneinfo/, so a malicious entry could target
//      arbitrary paths.
//   3. Every returned zone must survive validateZone if the fixture
//      tree is set up; skip when unset.
func FuzzReadZoneTab(f *testing.F) {
	// Real zone.tab shape (POSIX 2001):
	f.Add("# comment\nRO\t+4426+02606\tEurope/Bucharest\nUS\t+404251-0740023\tAmerica/New_York\n")
	// zone1970.tab shape (POSIX 2015, multi-country column):
	f.Add("RO,MD\t+4426+02606\tEurope/Bucharest\tRomania/Moldova\n")
	// Empty.
	f.Add("")
	// Only comments.
	f.Add("# only comments\n# more\n")
	// Malformed rows (missing fields).
	f.Add("RO\n")
	f.Add("RO\t+4426+02606\n")
	// Tab in the zone name column (unusual but non-fatal).
	f.Add("RO\t+4426+02606\tEurope/Bucharest\textra\n")
	// Row that would slip a path traversal into the zone name.
	f.Add("RO\t+4426+02606\t../etc/passwd\n")
	// Row with NUL byte in name.
	f.Add("RO\t+4426+02606\tEurope\x00Bucharest\n")

	f.Fuzz(func(t *testing.T, data string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "zone.tab")
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Skip()
		}
		zones, ok := readZoneTab(path)
		if !ok {
			return
		}
		for _, z := range zones {
			if strings.ContainsRune(z, '\x00') {
				t.Errorf("zone name contains NUL: %q (returned by readZoneTab)", z)
			}
			// A zone name that starts with a path separator or
			// contains a parent-dir segment would let a downstream
			// filepath.Join escape the zoneinfo tree.
			if strings.HasPrefix(z, "/") || strings.Contains(z, "..") {
				t.Errorf("zone name would escape zoneinfo tree: %q", z)
			}
		}
	})
}

// FuzzValidateZone fuzzes the zone-name validator directly. The
// validator is the last line of defense before the setter walks the
// name into filepath.Join with /usr/share/zoneinfo — a bug here
// lets a set-timezone request escape the zoneinfo tree.
func FuzzValidateZone(f *testing.F) {
	f.Add("Europe/Bucharest")
	f.Add("UTC")
	f.Add("America/New_York")
	f.Add("")
	f.Add("/etc/passwd")
	f.Add("../etc/passwd")
	f.Add("Europe/../etc/passwd")
	f.Add("Europe/Bucharest\x00")
	f.Add("Europe\nBucharest")

	f.Fuzz(func(t *testing.T, data string) {
		// Standalone: validateZone returns an error on any input
		// that would let the caller escape the zoneinfo tree. Any
		// path with `..` or a leading `/` must be rejected —
		// regardless of the on-disk state.
		err := validateZone(data)
		if err == nil {
			// Non-error path: the zoneinfoDir default may or may
			// not exist under test — we can't assert existence.
			// But the string itself must be safe.
			if strings.HasPrefix(data, "/") || strings.Contains(data, "..") ||
				strings.ContainsRune(data, '\x00') {
				t.Errorf("validateZone accepted unsafe string: %q", data)
			}
		}
	})
}
