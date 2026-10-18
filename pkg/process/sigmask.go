package process

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// SignalDisposition is what a process has arranged for one signal,
// as far as /proc/<pid>/status can say.
type SignalDisposition int

const (
	// SignalDefault: neither caught, blocked nor ignored — the signal
	// runs its default action, which for most signals is to terminate.
	SignalDefault SignalDisposition = iota
	// SignalHandled: caught or blocked. A reload reaches the service,
	// or waits; either way it does not kill it.
	SignalHandled
	// SignalIgnored: SIG_IGN. The signal is discarded, so a reload
	// neither kills nor reloads — it does nothing at all.
	//
	// This is a distinct case because it is the one where the obvious
	// warning would be false. SIG_IGN appears in SigIgn, not in SigCgt
	// or SigBlk, so a check that reads only those two masks concludes
	// "default action" and announces that a reload would terminate the
	// service. It would not.
	SignalIgnored
)

// ReloadSignalDisposition reports what pid has arranged for sig,
// according to the SigCgt, SigBlk and SigIgn masks in
// /proc/<pid>/status.
//
// It exists for one question asked at readiness time: a service that
// has a reload-signal but neither catches nor blocks it will not be
// reloaded by that signal — it will be killed by it, because the
// default action for most signals is to terminate. The service runs
// fine until the first reload, which is what makes it worth saying out
// loud while the service is starting rather than leaving it to be
// discovered later.
//
// An error means "could not tell", not "not handled". Callers warn
// only on a confident false, so an unreadable or oddly-shaped status
// file stays quiet instead of crying wolf.
func ReloadSignalDisposition(pid int, sig syscall.Signal) (SignalDisposition, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return SignalDefault, err
	}
	return reloadSignalDispositionIn(string(data), sig)
}

// reloadSignalDispositionIn is the pure half, so a test can state the
// /proc text verbatim — tabs included — instead of re-deriving it.
func reloadSignalDispositionIn(status string, sig syscall.Signal) (SignalDisposition, error) {
	if sig <= 0 || sig > 64 {
		return SignalDefault, fmt.Errorf("signal %d out of range for a 64-bit mask", sig)
	}
	// Signal N lives in bit N-1: SIGHUP (1) is bit 0.
	bit := uint64(1) << (uint(sig) - 1)

	var found, ignored bool
	for _, line := range strings.Split(status, "\n") {
		// /proc/<pid>/status separates the key from the value with a
		// TAB, not a space. strings.Fields splits on any whitespace,
		// which is why this does not try to trim a fixed prefix.
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "SigCgt:", "SigBlk:", "SigIgn:":
		default:
			continue
		}
		mask, err := strconv.ParseUint(fields[1], 16, 64)
		if err != nil {
			return SignalDefault, fmt.Errorf("%s %q: %w", fields[0], fields[1], err)
		}
		found = true
		if mask&bit == 0 {
			continue
		}
		if fields[0] == "SigIgn:" {
			// Keep looking: caught or blocked outranks ignored, and
			// the masks are not ordered in the file.
			ignored = true
			continue
		}
		return SignalHandled, nil
	}
	if !found {
		return SignalDefault, fmt.Errorf("no signal mask lines in status")
	}
	if ignored {
		return SignalIgnored, nil
	}
	return SignalDefault, nil
}
