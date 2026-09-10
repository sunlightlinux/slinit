package shutdown

import (
	"fmt"
	"os"
	"strings"
)

// powerStatePath is the sysfs entry the kernel exposes for suspend /
// standby / freeze / hibernate. Overridable for tests.
var powerStatePath = "/sys/power/state"

// suspendAllowedStates lists the kernel-defined values we accept
// straight through. Passing anything else risks a stray write to
// sysfs; the /sys/power/state parser rejects unknown strings with
// EINVAL, but validating upfront gives a clearer error message.
// Ordering matches Documentation/admin-guide/pm/sleep-states.rst.
var suspendAllowedStates = map[string]struct{}{
	"freeze":  {}, // suspend-to-idle (s2idle)
	"standby": {}, // power-on suspend
	"mem":     {}, // suspend-to-RAM (s3)
	"disk":    {}, // hibernate (s4). Does not return on success.
}

// Suspend writes `state` to /sys/power/state, putting the system to
// sleep. Blocks until wake for freeze/standby/mem; returns EINVAL if
// the state isn't supported by the kernel or the request is
// malformed. finit-parity for `initctl suspend`. Callers: the
// CmdSuspend control handler in pkg/control.
func Suspend(state string) error {
	state = strings.TrimSpace(state)
	if state == "" {
		state = "mem"
	}
	if _, ok := suspendAllowedStates[state]; !ok {
		return fmt.Errorf("suspend: unknown state %q (want freeze|standby|mem|disk)", state)
	}
	// Read supported states to give a specific error when the
	// kernel isn't configured for the requested mode (e.g. mem on
	// a board without S3 support). Best-effort: if the read fails,
	// fall through to the write and let the kernel produce EINVAL.
	if data, err := os.ReadFile(powerStatePath); err == nil {
		if !stateIsSupported(state, string(data)) {
			return fmt.Errorf("suspend: kernel does not support %q (supported: %q)",
				state, strings.TrimSpace(string(data)))
		}
	}
	if err := os.WriteFile(powerStatePath, []byte(state), 0); err != nil {
		return fmt.Errorf("suspend: write %s: %w", powerStatePath, err)
	}
	return nil
}

// stateIsSupported checks whether the requested state appears in the
// space-separated list the kernel prints in /sys/power/state.
func stateIsSupported(state, kernelList string) bool {
	for _, tok := range strings.Fields(kernelList) {
		if tok == state {
			return true
		}
	}
	return false
}
