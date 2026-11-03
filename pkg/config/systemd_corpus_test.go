package config

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// unitPlaceholder matches systemd's build-time substitutions in a
// .service.in file: {{NAME}} and @NAME@.
var unitPlaceholder = regexp.MustCompile(`\{\{[A-Z_]+\}\}|@[A-Za-z_]+@`)

// Every unit systemd itself ships must load. Run over the source tree
// next to this one (or $SLINIT_UNIT_CORPUS, a colon-separated list of
// directories), and skipped when there is none. It found, in one pass:
// systemd time spans (TimeoutStopSec=6min) copied through, five
// SystemCallFilter= groups and two x86_64 syscalls slinit did not know,
// an empty Delegate= refused, and UMask=7777 refused.
//
// Template units (foo@.service) are not supported by the direct loader
// yet, and are skipped.
func TestSystemdShippedUnitsLoad(t *testing.T) {
	roots := filepath.SplitList(os.Getenv("SLINIT_UNIT_CORPUS"))
	if len(roots) == 0 {
		roots = []string{filepath.Join("..", "..", "..", "systemd", "units")}
	}
	tmp := t.TempDir()
	loaded := 0
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			base := d.Name()
			in := strings.HasSuffix(base, ".service.in")
			if (!strings.HasSuffix(base, ".service") && !in) || strings.Contains(base, "@") {
				return nil
			}
			src := p
			if in {
				b, err := os.ReadFile(p)
				if err != nil {
					return nil
				}
				// The one substitution a unit's value depends on; the
				// rest are paths, and any path will do.
				b = bytes.ReplaceAll(b, []byte("{{HIGH_RLIMIT_NOFILE}}"), []byte("524288"))
				b = unitPlaceholder.ReplaceAll(b, []byte("/usr/lib/systemd"))
				base = strings.TrimSuffix(base, ".in")
				src = filepath.Join(tmp, base)
				if err := os.WriteFile(src, b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
				return nil
			}
			loaded++
			if _, err := SystemdUnitToServiceDescription(src, strings.TrimSuffix(base, ".service")); err != nil {
				t.Errorf("%s: %s", p, strings.SplitN(err.Error(), "\n", 2)[0])
			}
			return nil
		})
	}
	if loaded == 0 {
		t.Skip("no systemd unit tree found (set SLINIT_UNIT_CORPUS)")
	}
	t.Logf("%d units loaded", loaded)
}
