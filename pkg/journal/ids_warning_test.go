package journal

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// The missing-machine-id warning is worth printing on a normal boot: it
// means the journal's host identity changes on every reboot, and
// slinit-init-maker fixes it. It is not worth printing in a container,
// where images routinely ship without the file and the operator has
// nothing to act on — it was the first line of every `docker logs`.
func TestTransientIDWarningCanBeSilenced(t *testing.T) {
	var buf bytes.Buffer
	oldOut, oldOn, oldPath := warnOut, transientIDWarning, MachineIDPath
	t.Cleanup(func() {
		warnOut, transientIDWarning, MachineIDPath = oldOut, oldOn, oldPath
	})
	warnOut = &buf
	MachineIDPath = filepath.Join(t.TempDir(), "machine-id") // absent

	transientIDWarning = true
	if _, err := resolveMachineID(); err != nil {
		t.Fatalf("resolveMachineID: %v", err)
	}
	if !strings.Contains(buf.String(), "transient machine ID") {
		t.Errorf("warning missing when warnings are on; got %q", buf.String())
	}

	buf.Reset()
	SetTransientIDWarning(false)
	if _, err := resolveMachineID(); err != nil {
		t.Fatalf("resolveMachineID: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("warning printed after SetTransientIDWarning(false): %q", buf.String())
	}
}
