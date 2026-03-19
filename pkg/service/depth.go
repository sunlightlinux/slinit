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
	changed []depSnapshot
	committed bool
}

type depSnapshot struct {
	svc       Service
	origDepth int
}

// AddPotentialUpdate queues a service for depth recalculation.
func (u *DepDepthUpdater) AddPotentialUpdate(svc Service) {
	u.changed = append(u.changed, depSnapshot{svc: svc, origDepth: svc.Record().DepDepth()})
}

// ProcessUpdates recalculates depths for all queued services and propagates
// changes to their dependents (transitively). Returns an error if any service
// exceeds MaxDepDepth; the caller should then call Rollback().
func (u *DepDepthUpdater) ProcessUpdates() error {
	// Process queue (may grow as we add dependents)
	for i := 0; i < len(u.changed); i++ {
		svc := u.changed[i].svc
		rec := svc.Record()

		newDepth := calcDepth(rec)

		if newDepth > MaxDepDepth {
			return fmt.Errorf("service '%s': maximum dependency depth exceeded (%d)", rec.Name(), MaxDepDepth)
		}

		if newDepth != rec.DepDepth() {
			rec.SetDepDepth(newDepth)
			// Queue all direct dependents for recalculation
			for _, dept := range rec.Dependents() {
				if !u.hasService(dept.From) {
					u.changed = append(u.changed, depSnapshot{
						svc:       dept.From,
						origDepth: dept.From.Record().DepDepth(),
					})
				}
			}
		}
	}
	return nil
}

// Commit makes all depth changes permanent. After Commit(), Rollback() is a no-op.
func (u *DepDepthUpdater) Commit() {
	u.committed = true
	u.changed = nil
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
}

func (u *DepDepthUpdater) hasService(svc Service) bool {
	for _, snap := range u.changed {
		if snap.svc == svc {
			return true
		}
	}
	return false
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
