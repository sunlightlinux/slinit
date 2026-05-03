package snapshot

import (
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Capture walks the live ServiceSet and builds a Snapshot describing
// the operator-visible intent: which services were activated, pinned,
// or triggered, plus the global environment.
//
// Internal services and unloaded placeholders are skipped — they have
// no meaningful "intent" the operator could have set; only loaded
// user-facing services are recorded. Aliases (provides) are not
// emitted as separate entries: the canonical name is enough.
//
// Capture is read-only with respect to the ServiceSet. It must be
// called from a context where the set is quiescent enough to read
// per-service flags safely; in practice the daemon calls it from the
// shutdown path where no further state transitions are running.
func Capture(set *service.ServiceSet) *Snapshot {
	snap := &Snapshot{
		Version:   CurrentVersion,
		WrittenAt: time.Now().UTC().Format(time.RFC3339),
		GlobalEnv: append([]string(nil), set.GlobalEnv()...),
	}

	for _, svc := range set.ListServices() {
		entry := captureOne(svc)
		if entry == nil {
			continue
		}
		snap.Services = append(snap.Services, *entry)
	}

	return snap
}

// captureOne returns a ServiceSnapshot for svc, or nil if svc has no
// state worth preserving. Services that are not activated, pinned, or
// triggered are skipped — the dependency graph will pull them up
// transitively when their activator is re-started.
func captureOne(svc service.Service) *ServiceSnapshot {
	rec := svc.Record()

	activated := rec.IsMarkedActive()
	pinStart := rec.IsStartPinned()
	pinStop := rec.IsStopPinned()

	triggered := false
	if ts, ok := svc.(*service.TriggeredService); ok {
		triggered = ts.IsTriggered()
	}

	if !activated && !pinStart && !pinStop && !triggered {
		return nil
	}

	return &ServiceSnapshot{
		Name:        rec.Name(),
		Activated:   activated,
		PinnedStart: pinStart,
		PinnedStop:  pinStop,
		Triggered:   triggered,
	}
}
