package service

import "slices"

// conflicts (systemd's Conflicts=): starting a service stops the
// services it names, and the services that name it, as systemd's start
// job enqueues a stop job for each side of the conflict. It orders
// nothing: like systemd, the stop and the start run side by side. Kept
// by name, as part-of is (partof.go), so neither side need be loaded.
// When both are wanted, whichever starts last stays up; that is a
// configuration to fix, and each stop is logged with its reason.

// stopConflicting stops what svc conflicts with, in both directions.
// Called from initiateStart, under queueMu.
func (ss *ServiceSet) stopConflicting(svc Service) {
	own := svc.Record().conflicts
	name := svc.Name()
	ss.mu.RLock()
	var hit []Service
	for _, s := range ss.records {
		if s == svc || slices.Contains(hit, s) {
			continue
		}
		if slices.Contains(own, s.Name()) || slices.Contains(s.Record().conflicts, name) {
			hit = append(hit, s)
		}
	}
	ss.mu.RUnlock()
	for _, s := range hit {
		ss.stopForAnother(s, "it conflicts with '"+name+"', which is starting")
	}
}
