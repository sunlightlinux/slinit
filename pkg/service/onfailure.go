package service

import (
	"slices"
	"time"
)

// on-failure (systemd's OnFailure=): when a service ends failed, with
// no restart to come (its restart policy declined or its restart limit
// ran out), the services it names are started, as systemd starts the
// OnFailure= units when a unit enters the failed state. "Failed" is the
// reading failure-action uses (stoppedAs), except that a start failed
// by a dependency's failure counts only with failure-conditions =
// dependency, as systemd runs no OnFailure= for a dependency job.
//
// The starts are queued, not made from Stopped: a handler whose start
// fails at once (a missing required path, a false assert) would come
// back through Stopped inside the same drain of the queues, and a
// handler naming the failed service, or two naming each other, never
// left it, with queueMu held. processQueuesLocked starts them after the
// drain, and each handler may be started this way at most
// onFailureBurst times in onFailureInterval, systemd's default start
// limit, so a pair that keeps failing ends instead of respawning
// without limit. A service naming itself is skipped.

const (
	onFailureBurst    = 5
	onFailureInterval = 10 * time.Second
)

type onFailureStart struct {
	from string
	name string
	why  string // "" for on-failure, "upholds" (upholds.go)
}

// queueOnFailure queues svc's on-failure starts. Caller holds queueMu.
func (ss *ServiceSet) queueOnFailure(svc Service) {
	for _, name := range svc.Record().onFailure {
		if name == svc.Name() {
			ss.logger.Error("Service '%s': on-failure names the service itself; skipped", svc.Name())
			continue
		}
		ss.onFailureQueue = append(ss.onFailureQueue, onFailureStart{from: svc.Name(), name: name})
	}
}

// startQueuedOnFailure starts what queueOnFailure queued and reports
// whether it started anything, so the queues are drained again. Caller
// holds queueMu.
func (ss *ServiceSet) startQueuedOnFailure() bool {
	if len(ss.onFailureQueue) == 0 {
		return false
	}
	q := ss.onFailureQueue
	ss.onFailureQueue = nil
	now := time.Now()
	for _, st := range q {
		h, err := ss.LoadService(st.name)
		if err != nil {
			ss.logger.Error("Service '%s': on-failure: cannot load '%s': %v", st.from, st.name, err)
			continue
		}
		r := h.Record()
		r.onFailureStarts = slices.DeleteFunc(r.onFailureStarts, func(t time.Time) bool {
			return now.Sub(t) >= onFailureInterval
		})
		what := "on-failure"
		if st.why != "" {
			what = st.why
		}
		if st.why != "" && (h.State() != StateStopped || h.Record().pinnedStopped) {
			continue // upholds: up again by now, or pinned down
		}
		if len(r.onFailureStarts) >= onFailureBurst {
			ss.logger.Error("Service '%s': %s: '%s' started %d times in %v already; not again",
				st.from, what, st.name, onFailureBurst, onFailureInterval)
			continue
		}
		r.onFailureStarts = append(r.onFailureStarts, now)
		if st.why != "" {
			ss.logger.Info("Service '%s': starting '%s', which it upholds", st.from, st.name)
		} else {
			ss.logger.Info("Service '%s': failed; starting '%s' (on-failure)", st.from, st.name)
		}
		h.Start()
	}
	return true
}
