package service

// on-failure (systemd's OnFailure=): when a service ends failed, with
// no restart to come (its restart policy declined or its restart limit
// ran out), the services it names are started, as systemd starts the
// OnFailure= units when a unit enters the failed state. "Failed" is the
// reading failure-action uses (stoppedAsFailure), so the two always
// agree. A name that does not load is logged, as chain-to logs it.

// startOnFailure starts svc's on-failure services. Caller holds queueMu.
func (ss *ServiceSet) startOnFailure(svc Service) {
	for _, name := range svc.Record().onFailure {
		h, err := ss.LoadService(name)
		if err != nil {
			ss.logger.Error("Service '%s': on-failure: cannot load '%s': %v", svc.Name(), name, err)
			continue
		}
		ss.logger.Info("Service '%s': failed; starting '%s' (on-failure)", svc.Name(), name)
		h.Start()
	}
}
