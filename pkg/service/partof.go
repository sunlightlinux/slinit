package service

import "slices"

// part-of (systemd's PartOf=): a service names others whose stop or
// restart reaches it, without depending on them. A stop asked of one of
// them stops it; a restart of one, automatic or asked for, restarts it
// if it is running. It does not start with them, and a crash that
// leaves them stopped leaves it running, as in systemd, where the stop
// and restart jobs propagate and a unit's own death does not.
//
// The link is kept by name on the service that declares it and looked
// up when one of the named services stops, so it needs neither service
// loaded first, survives either being reloaded or unloaded, and adds
// nothing to the dependency graph (no start, no order, no edge a client
// sees).

// partOfMembers returns the loaded services that name svc in part-of.
// Called under queueMu; takes mu for the records, as
// releaseStopsLastLocked does.
func (ss *ServiceSet) partOfMembers(svc Service) []Service {
	name := svc.Name()
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	var out []Service
	for _, s := range ss.records {
		if s != svc && slices.Contains(s.Record().partOf, name) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// propagatePartOfStop stops the services part of svc, as stopDependents
// stops a dependent whose dependency is going away: the stop goes on to
// what depends on them, as systemd's stop job does.
func (ss *ServiceSet) propagatePartOfStop(svc Service) {
	for _, m := range ss.partOfMembers(svc) {
		ss.stopForAnother(m, "it is part of '"+svc.Name()+"'")
	}
}

// stopForAnother stops m because of another service (part-of,
// conflicts), as `slinitctl stop` would: its explicit activation goes
// and what depends on it stops too. One already stopped or on its way
// down is left alone, which also ends a loop. Caller holds queueMu.
func (ss *ServiceSet) stopForAnother(m Service, why string) {
	r := m.Record()
	if r.desired.Load() == StateStopped {
		return
	}
	if r.state.Load() == StateStopped && !r.restartPending {
		return
	}
	ss.logger.Info("Service '%s': stopping, as %s", m.Name(), why)
	r.desired.Store(StateStopped)
	if r.startExplicit {
		r.startExplicit = false
		r.Release(true)
	}
	r.stopReason = ReasonNormal
	r.propStop = true
	ss.AddPropQueue(m)
}

// propagatePartOfRestart restarts the running services part of svc,
// as systemd turns the restart into a try-restart of each: one that is
// not STARTED is left alone. A restart this one took part in carries
// its wave on, so a part-of loop restarts each service once.
// wave says svc's own restart came from one.
func (ss *ServiceSet) propagatePartOfRestart(svc Service, wave bool) {
	src := svc.Record()
	gen := src.partOfGen
	if !wave {
		ss.partOfGen++
		gen = ss.partOfGen
		src.partOfGen = gen
	}
	for _, m := range ss.partOfMembers(svc) {
		r := m.Record()
		if r.partOfGen == gen || r.state.Load() != StateStarted {
			continue
		}
		ss.logger.Info("Service '%s': restarting, as it is part of '%s'", m.Name(), svc.Name())
		r.partOfGen = gen
		r.inPartOfWave = true
		r.stopReason = ReasonDepRestart
		r.inUserRestart = true
		r.propStop = true
		ss.AddPropQueue(m)
	}
}
