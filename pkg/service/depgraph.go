package service

import "errors"

// Dependency-graph mutation, serialized.
//
// The graph is two slices per record: dependsOn on the source, dependents
// on the target. AddDep appends to both, RmDep splices both. Everything
// else in this package that touches them runs with queueMu held — the
// state machine holds it across whole ProcessQueues drains, and the
// monitor goroutines take it before mutating anything — but the control
// server does not: it hands every connection its own goroutine, and the
// add-dep, rm-dep and enable handlers called AddDep and RmDep straight
// from there.
//
// So two concurrent `slinitctl add-dep`, or one of them against a
// propagation pass walking the same graph after a process exit, is a data
// race on those slices. It is slices and not a map, so the consequence is
// not the immediate runtime fatal that an unguarded map write gives in
// PID 1 (see the extraEnv race) — it is a lost edge, or a walk indexing a
// slice that was reallocated under it. Quieter, and no more acceptable in
// PID 1.
//
// The exported functions here are the control path's way in. The lock
// cannot live in AddDep/RmDep themselves, nor in the loader: chain-to
// loads a service from inside the state machine (record.go, under
// queueMu), so a lock taken on that path would deadlock against itself.
// The invariant is the one the rest of the package already follows —
// mutators assume the caller holds the lock — and these are the callers
// that hold it.

// ErrDependencyCycle is returned when the requested edge would close a
// cycle in the graph.
var ErrDependencyCycle = errors.New("dependency would create a cycle")

// ErrDependencySelf is returned for an edge from a service to itself.
var ErrDependencySelf = errors.New("a service cannot depend on itself")

// WithGraphLock runs fn with the service-graph lock held, which makes
// everything fn does one atomic step with respect to the state machine.
//
// fn must not call back into anything that takes the lock itself —
// ProcessQueues, StartService, StopService or the other top-level entry
// points — because the lock is not reentrant. Use the locked internals,
// or arrange for the work to happen after fn returns.
func (ss *ServiceSet) WithGraphLock(fn func()) {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	fn()
}

// AddDependency installs an edge from→to of the given type, rejecting a
// self-edge, a cycle, or a graph deeper than MaxDepDepth.
//
// The circularity check and the append are one atomic step, which is the
// part that needs the lock rather than just mutual exclusion: two
// goroutines adding opposite edges could each pass a check against the
// pre-add graph and then both append, building the cycle that neither of
// them was allowed to build.
func (ss *ServiceSet) AddDependency(from, to Service, depType DependencyType) error {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	return ss.addDependencyLocked(from, to, depType)
}

// addDependencyLocked is AddDependency's body. Caller holds queueMu.
func (ss *ServiceSet) addDependencyLocked(from, to Service, depType DependencyType) error {
	if from == to {
		return ErrDependencySelf
	}
	if CheckCircularDep(from, to) {
		return ErrDependencyCycle
	}

	dep := from.Record().AddDep(to, depType)

	var updater DepDepthUpdater
	updater.AddPotentialUpdate(from)
	if err := updater.ProcessUpdates(); err != nil {
		// Remove the record we just added, by identity. The old control
		// handler rolled back with RmDep(to, depType), which removes the
		// FIRST edge matching that pair — not necessarily this one. With
		// a duplicate edge already present it could drop the older one
		// and leave this one behind still holding an acquisition that
		// nothing would ever release.
		from.Record().rmDepRecord(dep)
		updater.Rollback()
		// The removal released the target, which may have left a stop
		// queued. dinit drains here on the same path; so do we.
		ss.processQueuesLocked()
		return err
	}
	updater.Commit()
	return nil
}

// RemoveDependency removes an edge from→to of the given type and reports
// whether there was one to remove.
func (ss *ServiceSet) RemoveDependency(from, to Service, depType DependencyType) bool {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()

	if !from.Record().RmDep(to, depType) {
		return false
	}

	var updater DepDepthUpdater
	updater.AddPotentialUpdate(from)
	// from's dependents may get shallower too, so they are queued as well.
	for _, dept := range from.Record().Dependents() {
		updater.AddPotentialUpdate(dept.From)
	}
	if err := updater.ProcessUpdates(); err != nil {
		// Depths only decrease on a removal, so this should not happen;
		// rolling back is the safe response if it ever does.
		updater.Rollback()
	} else {
		updater.Commit()
	}

	// RmDep released the target synchronously, so a stop may be queued.
	ss.processQueuesLocked()
	return true
}

// EnsureWaitsFor installs a waits-for edge from→to unless from already
// holds a non-ordering dependency on to, and reports whether such an edge
// was already there.
//
// "Already there" is any non-BEFORE/AFTER dep of any type on the same
// target, matching dinit's add_service_dep: a WAITS_FOR request against a
// service that already has a REGULAR dep on that target is a no-op rather
// than a second edge. The existence check belongs under the same lock as
// the add, or two concurrent enables of one service both find nothing and
// both install an edge.
func (ss *ServiceSet) EnsureWaitsFor(from, to Service) (existed bool, err error) {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()

	for _, dep := range from.Record().Dependencies() {
		if dep.To == to && dep.DepType != DepBefore && dep.DepType != DepAfter {
			return true, nil
		}
	}
	return false, ss.addDependencyLocked(from, to, DepWaitsFor)
}

// DepView is a copy of one graph edge, safe to read after the lock has
// been dropped.
//
// Copies rather than *ServiceDep pointers because the records themselves
// keep changing under the lock: HoldingAcq is flipped during propagation,
// and a reload rewrites To on every edge pointing at the service being
// replaced (transferDependents). Handing a caller the pointer would hand
// it fields that move.
type DepView struct {
	From    Service
	To      Service
	DepType DependencyType
}

// DependenciesOf snapshots svc's outgoing edges under the graph lock.
//
// The control server's query-dependencies handler iterated the live slice
// from its own goroutine while the state machine appended to it, which is
// the same race as the mutating handlers had, just on the reading side.
func (ss *ServiceSet) DependenciesOf(svc Service) []DepView {
	ss.queueMu.RLock()
	defer ss.queueMu.RUnlock()
	deps := svc.Record().Dependencies()
	out := make([]DepView, 0, len(deps))
	for _, dep := range deps {
		out = append(out, DepView{From: dep.From, To: dep.To, DepType: dep.DepType})
	}
	return out
}

// DependentsOf snapshots the edges pointing at svc, under the graph lock.
func (ss *ServiceSet) DependentsOf(svc Service) []DepView {
	ss.queueMu.RLock()
	defer ss.queueMu.RUnlock()
	depts := svc.Record().Dependents()
	out := make([]DepView, 0, len(depts))
	for _, dep := range depts {
		out = append(out, DepView{From: dep.From, To: dep.To, DepType: dep.DepType})
	}
	return out
}

// WouldCycle reports whether an edge from→to would close a cycle, taken
// under the graph lock.
//
// Only useful for asking the question; a caller that intends to add the
// edge must use AddDependency, which asks it under the same lock as the
// append.
func (ss *ServiceSet) WouldCycle(from, to Service) bool {
	ss.queueMu.RLock()
	defer ss.queueMu.RUnlock()
	return CheckCircularDep(from, to)
}
