package snapshot

import (
	"fmt"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// RestoreLogger is the minimal logging surface Restore needs. The
// daemon passes its real logger; tests pass nil to silence output.
type RestoreLogger interface {
	Notice(format string, args ...any)
	Info(format string, args ...any)
	Warn(format string, args ...any)
}

// Restore applies snap to set: re-arms global env, re-applies pins
// and triggers, then re-activates services that were explicitly
// started before the snapshot was written.
//
// It is safe to call before the event loop starts — no concurrent
// state-machine work is expected at restore time. The caller is
// responsible for having already loaded service descriptions (via
// the loader) so Restore can resolve names; entries naming an unknown
// service are logged and skipped rather than failing the whole boot.
//
// Returns the number of services whose intent was applied. The error
// is non-nil only for catastrophic problems — unknown entries are not
// fatal.
//
// # Phase B forward-compat
//
// This function is intentionally per-service: each entry is applied
// in isolation, with no cross-service state shared between iterations.
// When PID re-attach lands, the only change here is a branch inside
// the per-entry block: "if entry.PID > 0 and /proc/<pid> exists →
// attach instead of Start". Nothing in the surrounding scaffolding
// needs to move.
func Restore(set *service.ServiceSet, snap *Snapshot, logger RestoreLogger) (int, error) {
	if snap == nil {
		return 0, fmt.Errorf("nil snapshot")
	}

	// Global env first so any service that consults it during
	// BringUp sees the operator-set values.
	envApplied := 0
	for _, kv := range snap.GlobalEnv {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		set.GlobalSetEnv(k, v)
		envApplied++
	}

	applied := 0
	for _, entry := range snap.Services {
		if entry.Name == "" {
			continue
		}
		if !applyOne(set, entry, logger) {
			continue
		}
		applied++
	}

	// Drain anything pin/trigger/start enqueued.
	set.ProcessQueues()

	if logger != nil {
		logger.Notice("snapshot restore: %d service intents (of %d), %d global env vars",
			applied, len(snap.Services), envApplied)
	}
	return applied, nil
}

// applyOne re-applies the intent recorded in entry. Returns false if
// the entry could not be resolved (unknown service); the caller logs
// once at the higher level.
func applyOne(set *service.ServiceSet, entry ServiceSnapshot, logger RestoreLogger) bool {
	svc := set.FindService(entry.Name, false)
	if svc == nil {
		if logger != nil {
			logger.Warn("snapshot: service %q not loaded — skipping", entry.Name)
		}
		return false
	}

	// Pin first: PinnedStop wins if both happen to be true. This is
	// the safer default — refusing to auto-start a service the
	// operator deliberately pinned down.
	switch {
	case entry.PinnedStop:
		svc.Record().PinStop()
	case entry.PinnedStart:
		svc.Record().PinStart()
	}

	// Trigger: a TriggeredService remembers the latch even when
	// stopped, so SetTrigger before Start works in either order.
	if entry.Triggered {
		if ts, ok := svc.(*service.TriggeredService); ok {
			ts.SetTrigger(true)
		}
	}

	// Activation: skip if the operator pinned the service down — they
	// asked for it to stay stopped, intent should be preserved across
	// the restart.
	if entry.Activated && !entry.PinnedStop {
		set.StartService(svc)
	}

	return true
}
