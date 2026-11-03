package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The generated files must be what the generator makes from the systemd
// tree beside this one. Skipped without that tree; where it exists, a
// stale zgroups.go — systemd changed a group, nobody regenerated — fails
// here instead of drifting the way the hand-kept groups did.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	sd := filepath.Join("..", "..", "..", "systemd")
	if _, err := os.Stat(filepath.Join(sd, "src", "shared", "seccomp-util.c")); err != nil {
		t.Skip("no systemd tree beside slinit")
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
			t.Errorf("pkg/seccomp/%s is stale: run go run ./tools/seccompgroups -systemd ../systemd", name)
		}
	}
}
