package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// part-of, conflicts, on-failure and chain-to load whatever they name;
// a name that resolves to nothing is a warning, one service file away
// from a silent typo.
func TestCheckNamedRefs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "real"), []byte("type = internal\n"), 0o644)
	set := service.NewServiceSet(logging.New(logging.LevelError))
	loader := newCheckLoader(set, []string{dir})
	desc := &config.ServiceDescription{
		PartOf:    []string{"real", "rael"},
		Conflicts: []string{"real"},
		OnFailure: []string{"notfiy"},
		ChainTo:   "real",
	}
	if n := checkNamedRefs(loader, "svc", desc); n != 2 {
		t.Errorf("%d warnings, want 2 (rael, notfiy)", n)
	}
}
