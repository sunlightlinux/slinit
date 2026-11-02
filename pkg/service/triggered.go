package service

// TriggeredService is a service that waits for an external trigger before
// completing startup. Like InternalService, it has no external process.
// The trigger is set via SetTrigger(true), typically from the control
// socket (Phase 4) or programmatically.
type TriggeredService struct {
	ServiceRecord
	isTriggered bool
}

// NewTriggeredService creates a new triggered service.
func NewTriggeredService(set *ServiceSet, name string) *TriggeredService {
	svc := &TriggeredService{}
	svc.ServiceRecord = *NewServiceRecord(svc, set, name, TypeTriggered)
	return svc
}

// BringUp starts the triggered service. If already triggered, transitions to
// STARTED immediately. Otherwise, stays in STARTING state until triggered.
func (s *TriggeredService) BringUp() bool {
	// Predicates first, as for every other type since 3.7.0: a false
	// condition skips the service at once (STARTED, marked skipped)
	// rather than leaving it waiting for a trigger, a false assert fails
	// it.
	switch outcome, reason := s.CheckPredicates(); outcome {
	case PredFailed:
		s.services.logger.Error("Service '%s': %s", s.serviceName, reason)
		return false
	case PredSkip:
		s.services.logger.Info("Service '%s': skipped (%s)", s.serviceName, reason)
		s.markSkippedStartReason(reason)
		return true
	}
	if s.isTriggered {
		s.Started()
	}
	// If not triggered, we stay in STARTING state until SetTrigger(true)
	return true
}

// BringDown stops the triggered service immediately.
func (s *TriggeredService) BringDown() {
	s.Stopped()
}

// CanInterruptStart returns true since there is no process to interrupt.
func (s *TriggeredService) CanInterruptStart() bool {
	return true
}

// InterruptStart cancels the start immediately.
func (s *TriggeredService) InterruptStart() bool {
	return true
}

// SetTrigger sets or clears the trigger. When set to true and the service
// is in STARTING state with deps satisfied, the service transitions to STARTED.
func (s *TriggeredService) SetTrigger(triggered bool) {
	s.isTriggered = triggered
	if s.isTriggered && s.state.Load() == StateStarting && !s.waitingForDeps {
		s.Started()
	}
}

// IsTriggered returns the current trigger state.
func (s *TriggeredService) IsTriggered() bool {
	return s.isTriggered
}
