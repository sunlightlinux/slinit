package shutdown

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/process"
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

// sleepHookPaths is where a pre/post sleep hook is looked for, first
// match wins. Same shape as shutdownHookPaths and deliberately a single
// file rather than systemd's directory of scripts: slinit's house
// convention is one hook, and a hook that needs to fan out to several
// scripts can source a directory itself — which is what sunlight-os
// already does for its shutdown hook.
var sleepHookPaths = []string{
	"/etc/slinit/sleep-hook",
	"/lib/slinit/sleep-hook",
}

// Suspend writes `state` to /sys/power/state, putting the system to
// sleep. Blocks until wake for freeze/standby/mem; returns EINVAL if
// the state isn't supported by the kernel or the request is
// malformed. finit-parity for `initctl suspend`. Callers: the
// CmdSuspend control handler in pkg/control.
//
// The sleep hook runs before the write and again after the kernel
// returns, which for suspend-to-RAM is after the machine has woken. A
// nil logger silences the hook's own output but still runs it.
//
// This is the one place the kernel write happens, so it is also the one
// place the hook can be guaranteed to bracket it — slinit-logind routes
// its D-Bus Suspend() through here rather than writing sysfs itself, so
// a lid close and `slinitctl suspend` get the same hooks.
func Suspend(state string, logger *logging.Logger) error {
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
	runSleepHook("pre", state, logger)
	err := os.WriteFile(powerStatePath, []byte(state), 0)
	// The post hook runs even when the write failed, so a pre hook that
	// stopped something always gets its counterpart. Scripts tell the
	// two apart by $1.
	runSleepHook("post", state, logger)
	if err != nil {
		return fmt.Errorf("suspend: write %s: %w", powerStatePath, err)
	}
	return nil
}

// sleepOperationArg maps a kernel sleep state to the operation name
// systemd passes its system-sleep scripts, so a script copied from
// /usr/lib/systemd/system-sleep/ reads the argument it expects.
func sleepOperationArg(state string) string {
	if state == "disk" {
		return "hibernate"
	}
	return "suspend"
}

// runSleepHook invokes the sleep hook as `hook <phase> <operation>
// <state>`: phase is "pre" or "post", operation is systemd's vocabulary
// for script compatibility, and state is the raw kernel token for
// scripts that need to tell s2idle from S3.
//
// A failing hook is logged and otherwise ignored, matching systemd. The
// alternative — aborting the suspend — is worse on the hardware this is
// for: a laptop whose lid is shut and which then stays awake because a
// script exited non-zero cooks itself in a bag.
func runSleepHook(phase, state string, logger *logging.Logger) {
	var hookPath string
	for _, path := range sleepHookPaths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			continue
		}
		hookPath = path
		break
	}
	if hookPath == "" {
		return
	}

	op := sleepOperationArg(state)
	if logger != nil {
		logger.Notice("Running sleep hook: %s %s %s %s", hookPath, phase, op, state)
	}

	cmd := exec.Command(hookPath, phase, op, state)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := process.RunAdhoc(cmd)

	if logger != nil {
		for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
			if len(line) > 0 {
				logger.Info("sleep-hook: %s", string(line))
			}
		}
		if err != nil {
			logger.Error("Sleep hook %s failed: %v (continuing — a stuck "+
				"suspend is worse than a failed script)", phase, err)
		}
	}
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
