package config

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/internal/reftree"
)

// unitPlaceholder matches systemd's build-time substitutions in a
// .service.in file: {{NAME}} and @NAME@.
var unitPlaceholder = regexp.MustCompile(`\{\{[A-Z_]+\}\}|@[A-Za-z_]+@`)

// knownRefusedUnits are the shipped units slinit refuses on purpose,
// with the text the refusal must hold. Refusing is fail-closed: the
// unit asks for confinement slinit cannot express, and loading it
// would run it with more privilege than it asks for.
var knownRefusedUnits = map[string]string{
	// CapabilityBoundingSet= (empty): slinit's capability-bounding-set
	// cannot express an empty set.
	"systemd-localed.service": "CapabilityBoundingSet= leaves no capability",
}

// unitCorpusRoots are the systemd unit trees the corpus test reads:
// $SLINIT_UNIT_CORPUS, a colon-separated list, or else the units of the
// systemd source tree reftree.Systemd finds ($SLINIT_SYSTEMD_SRC, or
// where the checkouts put it).
func unitCorpusRoots() []string {
	if roots := filepath.SplitList(os.Getenv("SLINIT_UNIT_CORPUS")); len(roots) > 0 {
		return roots
	}
	if sd, ok := reftree.Systemd(); ok {
		return []string{filepath.Join(sd, "units")}
	}
	return nil
}

// Every unit systemd itself ships must load, but for knownRefusedUnits,
// which must be refused for their reason. Run over unitCorpusRoots, and
// skipped when there is none. It found, in one pass:
// systemd time spans (TimeoutStopSec=6min) copied through, five
// SystemCallFilter= groups and two x86_64 syscalls slinit did not know,
// an empty Delegate= refused, and UMask=7777 refused.
//
// Template units (foo@.service) are not supported by the direct loader
// yet, and are skipped.
func TestSystemdShippedUnitsLoad(t *testing.T) {
	tmp := t.TempDir()
	loaded := 0
	refused := map[string]bool{}
	for _, root := range unitCorpusRoots() {
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
			_, loadErr := SystemdUnitToServiceDescription(src, strings.TrimSuffix(base, ".service"))
			why, known := knownRefusedUnits[base]
			switch {
			case known && loadErr == nil:
				t.Errorf("%s: loads, but is listed in knownRefusedUnits (%s) — drop it from the list if that is right", p, why)
			case known && !strings.Contains(loadErr.Error(), why):
				t.Errorf("%s: refused, but not for the known reason %q: %s", p, why, strings.SplitN(loadErr.Error(), "\n", 2)[0])
			case known:
				refused[base] = true
			case loadErr != nil:
				t.Errorf("%s: %s", p, strings.SplitN(loadErr.Error(), "\n", 2)[0])
			}
			return nil
		})
	}
	if loaded == 0 {
		t.Skip("no systemd unit tree found (set SLINIT_UNIT_CORPUS or " + reftree.SystemdEnv + ")")
	}
	t.Logf("%d units read, %d refused as expected", loaded, len(refused))
}
