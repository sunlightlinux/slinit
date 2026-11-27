package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// Rescue mode runs `mount -a` with a timeout so that a mount hanging on
// an unreachable NFS server cannot keep the operator from the shell.
// mount forks a helper (mount.nfs) that holds the output pipe; killing
// mount at the timeout left CombinedOutput waiting for that pipe, and
// the rescue shell never came up.
func TestMountLocalFsBestEffortTimeoutWithLingeringHelper(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "mount")
	// The helper outlives the killed mount and keeps stdout open.
	script := "#!/bin/sh\nsleep 20 &\nexec sleep 20\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	origC, origT := mountCandidates, mountAllTimeout
	mountCandidates, mountAllTimeout = []string{fake}, 200*time.Millisecond
	defer func() { mountCandidates, mountAllTimeout = origC, origT }()

	done := make(chan struct{})
	go func() {
		mountLocalFsBestEffort(logging.New(logging.LevelError))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("mountLocalFsBestEffort did not return after its timeout: the rescue shell would never start")
	}
}
