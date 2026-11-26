package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/internal/reftree"
)

// The generated files must be what the generator makes from the systemd
// tree (reftree.Systemd: $SLINIT_SYSTEMD_SRC or a sibling checkout).
// Skipped without that tree; where it exists, a stale zgroups.go —
// systemd changed a group, nobody regenerated — fails here instead of
// drifting the way the hand-kept groups did.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	sd, ok := reftree.Systemd()
	if !ok {
		t.Skipf("no systemd source tree: set %s or check systemd out beside slinit", reftree.SystemdEnv)
	}
	files, _, err := generate(sd)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join("..", "..", "pkg", "seccomp", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("pkg/seccomp/%s is stale: run go run ./tools/seccompgroups -systemd %s", name, sd)
		}
	}
}
