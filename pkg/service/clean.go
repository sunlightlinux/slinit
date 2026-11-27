package service

import (
	"errors"
	"fmt"
	"os"
)

// CleanService removes the directories of a stopped service whose kind
// what selects (runtime, state, cache, logs, configuration: the
// *-directory settings) and, with fdstore, the descriptors its fd store
// keeps: systemd's `systemctl clean`. It returns what it removed, a
// directory's path or "fdstore". A service that is not STOPPED, or one
// parked on a restart-delay, is refused: its process may be using them.
//
// Called by a control connection, so it takes queueMu: the state read
// and the removal are one step against a start that could otherwise
// create the directories again half way.
func (ss *ServiceSet) CleanService(svc Service, kinds map[string]bool, fdstore bool) ([]string, error) {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	rec := svc.Record()
	if st := rec.state.Load(); st != StateStopped || rec.restartPending {
		return nil, fmt.Errorf("service '%s' is %s: stop it first", svc.Name(), stateWord(st, rec.restartPending))
	}
	var removed []string
	var errs []error
	for _, d := range rec.serviceDirs {
		if !kinds[d.Kind] {
			continue
		}
		if _, err := os.Lstat(d.Path); err != nil {
			continue // nothing there
		}
		if err := os.RemoveAll(d.Path); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, d.Path)
	}
	if fdstore && rec.fdStore != nil && rec.fdStore.Len() > 0 {
		rec.fdStore.Close()
		removed = append(removed, "fdstore")
	}
	if len(removed) > 0 {
		ss.logger.Info("Service '%s': cleaned %v", svc.Name(), removed)
	}
	return removed, errors.Join(errs...)
}

func stateWord(st ServiceState, restartPending bool) string {
	if restartPending {
		return "waiting to restart"
	}
	switch st {
	case StateStarting:
		return "starting"
	case StateStarted:
		return "started"
	case StateStopping:
		return "stopping"
	}
	return "not stopped"
}
