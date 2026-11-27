package journal

import (
	"bytes"
	"os"
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

// A malformed /etc/machine-id made InitIDs return before marking the
// cache initialised, and the getters panic until it is: the first
// service event then panicked PID 1, which is a kernel panic at boot.
// InitIDs still reports the problem, but the IDs are usable.
func TestInitIDsMalformedMachineIDStillInitialises(t *testing.T) {
	oldPath := MachineIDPath
	resetIDsForTest()
	t.Cleanup(func() {
		MachineIDPath = oldPath
		resetIDsForTest()
	})
	MachineIDPath = filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(MachineIDPath, []byte("this is not a machine id\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InitIDs("h"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Errorf("InitIDs: err = %v, want the malformed machine-id reported", err)
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("getter panicked after InitIDs reported an error: %v", r)
			}
		}()
		if m := MachineID(); !isValidID(m) {
			t.Errorf("MachineID = %q, want a valid transient ID", m)
		}
		if b := BootID(); !isValidID(b) {
			t.Errorf("BootID = %q, want a valid ID", b)
		}
		if h := Hostname(); h != "h" {
			t.Errorf("Hostname = %q, want h", h)
		}
	}()
}
