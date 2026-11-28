package service

import "slices"

// upholds (systemd's Upholds=): while a service is STARTED, the services
// it names are started whenever they are found stopped, as systemd
// starts an upheld unit whenever it is inactive or failed, with no
// delay and the usual rate limit. By name, like part-of (partof.go).
// "Found stopped" is two moments: the upholder reaching STARTED, and an
// upheld service reaching STOPPED while an upholder is up. The starts
// go through the on-failure queue (onfailure.go): after the drain of
// the queues, and at most 5 in 10 seconds per service, so one that
// cannot stay up comes to rest instead of looping.

// upholdOnStarted queues the start of what svc upholds. Caller holds
// queueMu.
func (ss *ServiceSet) upholdOnStarted(svc Service) {
	if ss.IsShuttingDown() {
		return
	}
	for _, name := range svc.Record().upholds {
		if name == svc.Name() {
			continue
		}
		if s := ss.FindService(name, false); s != nil && s.State() != StateStopped {
			continue
		}
		ss.onFailureQueue = append(ss.onFailureQueue, onFailureStart{from: svc.Name(), name: name, why: "upholds"})
	}
}

// upheldByActive reports whether a STARTED service upholds name. Caller
// holds queueMu; takes mu for the records.
func (ss *ServiceSet) upheldByActive(name string) bool {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	for _, s := range ss.records {
		if s.Name() != name && s.State() == StateStarted && slices.Contains(s.Record().upholds, name) {
			return true
		}
	}
	return false
}

// upholdOnStopped queues svc's start again if a STARTED service upholds
// it. Caller holds queueMu; takes mu for the records, as partOfMembers.
func (ss *ServiceSet) upholdOnStopped(svc Service) {
	name := svc.Name()
	ss.mu.RLock()
	var by string
	for _, s := range ss.records {
		if s != svc && s.State() == StateStarted && slices.Contains(s.Record().upholds, name) {
			by = s.Name()
			break
		}
	}
	ss.mu.RUnlock()
	if by != "" {
		ss.onFailureQueue = append(ss.onFailureQueue, onFailureStart{from: by, name: name, why: "upholds"})
	}
}
