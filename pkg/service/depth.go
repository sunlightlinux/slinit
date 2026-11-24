package service

import "fmt"

// MaxDepDepth is the maximum allowed dependency chain depth.
const MaxDepDepth = 32

// DepDepthUpdater recalculates dependency depths atomically with rollback.
//
// Usage:
//  1. Call AddPotentialUpdate(svc) for services whose depth may have changed.
//  2. Call ProcessUpdates() to recalculate and propagate. Returns an error
//     if MaxDepDepth is exceeded; in that case all changes are rolled back.
//  3. Call Commit() to make the changes permanent.
//
// If Commit() is never called, Rollback() restores all original depths.
type DepDepthUpdater struct {
	// snapshots of changed services and their original depths
	changed   []depSnapshot
	committed bool

	snapped map[Service]bool // services with a snapshot in changed
	queue   []Service        // services still to (re)calculate
	queued  map[Service]bool // services in queue
}

type depSnapshot struct {
	svc       Service
	origDepth int
}

// AddPotentialUpdate queues a service for depth recalculation.
func (u *DepDepthUpdater) AddPotentialUpdate(svc Service) {
	if u.snapped == nil {
		u.snapped = make(map[Service]bool)
		u.queued = make(map[Service]bool)
	}
	if !u.snapped[svc] {
		u.snapped[svc] = true
		u.changed = append(u.changed, depSnapshot{svc: svc, origDepth: svc.Record().DepDepth()})
	}
	if !u.queued[svc] {
		u.queued[svc] = true
		u.queue = append(u.queue, svc)
	}
}

// ProcessUpdates recalculates depths for all queued services and propagates
// changes to their dependents (transitively). Returns an error if any service
// exceeds MaxDepDepth; the caller should then call Rollback().
func (u *DepDepthUpdater) ProcessUpdates() error {
	// Process queue (may grow as we add dependents). A service already
	// calculated is queued again when one of its dependencies changes
	// later: in a diamond it can be reached through one path before the
	// other has its new depth.
	for len(u.queue) > 0 {
		svc := u.queue[0]
		u.queue = u.queue[1:]
		delete(u.queued, svc)
		rec := svc.Record()

		newDepth := calcDepth(rec)

		if newDepth > MaxDepDepth {
			return fmt.Errorf("service '%s': maximum dependency depth exceeded (%d)", rec.Name(), MaxDepDepth)
		}

		if newDepth != rec.DepDepth() {
			rec.SetDepDepth(newDepth)
			// Queue all direct dependents for recalculation
			for _, dept := range rec.Dependents() {
				u.AddPotentialUpdate(dept.From)
			}
		}
	}
	return nil
}

// Commit makes all depth changes permanent. After Commit(), Rollback() is a no-op.
func (u *DepDepthUpdater) Commit() {
	u.committed = true
	u.changed = nil
	u.snapped, u.queue, u.queued = nil, nil, nil
}

// Rollback restores all services to their original depths.
// This is a no-op if Commit() was called.
func (u *DepDepthUpdater) Rollback() {
	if u.committed {
		return
	}
	for _, snap := range u.changed {
		snap.svc.Record().SetDepDepth(snap.origDepth)
	}
	u.changed = nil
	u.snapped, u.queue, u.queued = nil, nil, nil
}

// calcDepth computes a service's depth as max(dep.depth + 1) over all deps.
func calcDepth(rec *ServiceRecord) int {
	depth := 0
	for _, dep := range rec.Dependencies() {
		d := dep.To.Record().DepDepth() + 1
		if d > depth {
			depth = d
		}
	}
	return depth
}

// CheckCircularDep checks whether adding a dependency from→to would create
// a cycle. It performs a BFS from to's dependencies looking for from.
func CheckCircularDep(from, to Service) bool {
	visited := map[Service]bool{}
	queue := []Service{to}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == from {
			return true
		}
		if visited[cur] {
			continue
		}
		visited[cur] = true
		for _, dep := range cur.Record().Dependencies() {
			if !visited[dep.To] {
				queue = append(queue, dep.To)
			}
		}
	}
	return false
}
