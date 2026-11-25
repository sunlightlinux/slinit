package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A service name is joined onto the service directories, and Join
// resolves "..": a name (or an instance argument) climbing out of the
// directory loaded a description from anywhere on the filesystem.
func TestLoadRefusesANameThatLeavesTheServiceDir(t *testing.T) {
	root := t.TempDir()
	svcDir := filepath.Join(root, "etc", "slinit.d")
	outside := filepath.Join(root, "tmp")
	for _, d := range []string{svcDir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "evil"), []byte("type = internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"a/../../../tmp/evil", "x@/../../../tmp/evil"} {
		ss := service.NewServiceSet(nil)
		dl := NewDirLoader(ss, []string{svcDir})
		ss.SetLoader(dl)
		if svc, err := dl.LoadService(name); err == nil {
			t.Errorf("LoadService(%q) loaded %s from outside %s", name, svc.Name(), svcDir)
		}
	}
}
