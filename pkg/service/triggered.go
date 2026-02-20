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
	if s.isTriggered && s.State() == StateStarting && !s.waitingForDeps {
		s.Started()
	}
}

// IsTriggered returns the current trigger state.
func (s *TriggeredService) IsTriggered() bool {
	return s.isTriggered
}
