package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// The tests run unprivileged, so "root" is whoever runs them. The one
// below that needs a different owner sets hookOwnerUID itself.
func init() { hookOwnerUID = uint32(os.Getuid()) }

// A script or directory not owned by root is refused, whatever its
// mode: its owner could rewrite it.
func TestHooks_SkipNotOwnedByRoot(t *testing.T) {
	orig := hookOwnerUID
	hookOwnerUID = uint32(os.Getuid()) + 1
	defer func() { hookOwnerUID = orig }()

	dir := t.TempDir()
	pointDir := filepath.Join(dir, "system-up")
	if err := os.MkdirAll(pointDir, 0o755); err != nil {
		t.Fatal(err)
	}
	SetHooksDir(dir)
	defer SetHooksDir("/etc/slinit/hooks.d")
	orig1, orig2 := rcLocalPath, rcLocalDPath
	rcLocalPath = filepath.Join(dir, "rc.local")
	rcLocalDPath = filepath.Join(dir, "rc.local.d")
	defer func() { rcLocalPath, rcLocalDPath = orig1, orig2 }()

	marker := filepath.Join(dir, "ran")
	writeMarkerScript(t, filepath.Join(pointDir, "10-x"), marker, 0o755)
	writeMarkerScript(t, rcLocalPath, marker, 0o755)

	logger := logging.New(logging.LevelDebug)
	if ran, _ := Run("system-up", logger); ran != 0 {
		t.Errorf("hook not owned by the owner uid ran (ran=%d)", ran)
	}
	if ran, _ := RunRcLocal(logger); ran != 0 {
		t.Errorf("rc.local not owned by the owner uid ran (ran=%d)", ran)
	}
	if ranScript(marker) {
		t.Error("a script not owned by the owner uid ran")
	}
}
