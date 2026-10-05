package shutdown

import (
	"bytes"
	"os"
	"os/exec"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/process"
)

// PowerState is what a UPS daemon has told init about mains power.
//
// The names are slinit's; the single letters they come from are
// sysvinit's, and they are what nut and apcupsd actually write.
type PowerState string

const (
	// PowerFailing — mains lost, the UPS is carrying the load.
	// sysvinit's `F`, and its powerwait/powerfail entries.
	PowerFailing PowerState = "failing"
	// PowerOK — mains restored. sysvinit's `O`, powerokwait.
	PowerOK PowerState = "ok"
	// PowerLow — mains lost and the battery is nearly out. sysvinit's
	// `L`, powerfailnow. The one that means "stop work now".
	PowerLow PowerState = "low"
)

// powerStatusPaths is where a UPS daemon leaves the reason for SIGPWR,
// most-current spelling first.
//
// sysvinit's manual documents /etc/powerstatus, but its source has
// preferred /var/run/powerstatus since 2010 and treats /etc as an
// obsolete fallback it warns about — the manual is behind the code.
// Both are accepted here, plus /run, which is where /var/run points on
// any system slinit runs on. Overridable for tests.
var powerStatusPaths = []string{
	"/run/powerstatus",
	"/var/run/powerstatus",
	"/etc/powerstatus",
}

// powerHookPaths mirrors the shutdown and sleep hooks: an executable
// the operator supplies, or nothing happens.
var powerHookPaths = []string{
	"/etc/slinit/power-hook",
	"/lib/slinit/power-hook",
}

// ReadPowerStatus consumes the status a UPS daemon left for us.
//
// One byte, as sysvinit reads it, and the file is removed afterwards
// because the status is an event rather than a state: the daemon writes
// it and then signals, so a file left in place would make the next
// SIGPWR report a power failure that had already been handled.
//
// Anything that is not F, O or L — including a missing or unreadable
// file — is reported as PowerFailing. That is sysvinit's documented
// behaviour and it is the safe direction to be wrong in: acting as
// though the power is failing when it is fine costs an unnecessary hook
// run, where the reverse costs the machine.
func ReadPowerStatus() PowerState {
	for _, path := range powerStatusPaths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var b [1]byte
		n, _ := f.Read(b[:])
		f.Close()
		// Removed whether or not the read worked. A file we cannot read
		// would otherwise be found again on every later signal.
		_ = os.Remove(path)
		if n != 1 {
			return PowerFailing
		}
		switch b[0] {
		case 'O', 'o':
			return PowerOK
		case 'L', 'l':
			return PowerLow
		default:
			return PowerFailing
		}
	}
	return PowerFailing
}

// RunPowerHook runs the operator's power hook with the state as its only
// argument: `failing`, `ok` or `low`.
//
// slinit does not act on the state itself — no automatic shutdown on
// PowerLow, however tempting. What a machine should do when its UPS
// battery runs down is a policy decision (finish a transaction, flush a
// cache, power off, ignore it because another host is responsible), and
// an init system that picks one silently will be wrong on some
// machines in the most expensive way available. The hook decides, and
// `slinitctl poweroff` from inside it is one line. This is the same
// stance the logind work took, where every Handle* key defaults to
// ignore rather than powering the machine off on a key press.
//
// A failing hook is logged and otherwise ignored, as with the sleep
// hook: by the time this runs the power is already gone, and refusing
// to continue helps nobody.
func RunPowerHook(state PowerState, logger *logging.Logger) {
	var hookPath string
	for _, path := range powerHookPaths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			continue
		}
		hookPath = path
		break
	}
	if hookPath == "" {
		if logger != nil {
			logger.Notice("Power status %s, but no power hook is installed "+
				"(looked for %s) — nothing to run", state, powerHookPaths[0])
		}
		return
	}

	if logger != nil {
		logger.Notice("Running power hook: %s %s", hookPath, state)
	}

	cmd := exec.Command(hookPath, string(state))
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := process.RunAdhoc(cmd)

	if logger != nil {
		for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
			if len(line) > 0 {
				logger.Info("power-hook: %s", string(line))
			}
		}
		if err != nil {
			logger.Error("Power hook failed for state %s: %v (continuing — "+
				"the power event has already happened)", state, err)
		}
	}
}

// HandlePowerSignal is the whole SIGPWR path: read why, tell the
// operator's hook. Exported so the event loop has one call to make.
func HandlePowerSignal(logger *logging.Logger) PowerState {
	state := ReadPowerStatus()
	RunPowerHook(state, logger)
	return state
}
