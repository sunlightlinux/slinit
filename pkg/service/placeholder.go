package service

// PlaceholderService stands in for a service that has been unloaded while
// something still refers to it, as dinit's placeholder_service does. Today
// that something is a log consumer: unloading a producer with
// `log-type = pipe` must not break the pipe its consumer reads, so the
// placeholder takes the pipe and the consumer link, and hands both to the
// producer when it is loaded again (dinit 1e50862, b58775b, issue #600).
//
// A placeholder is never started and is not listed; FindService skips it
// unless asked for placeholders.
type PlaceholderService struct {
	ServiceRecord
}

// NewPlaceholderService creates a placeholder named after the service it
// stands for.
func NewPlaceholderService(set *ServiceSet, name string) *PlaceholderService {
	svc := &PlaceholderService{}
	svc.ServiceRecord = *NewServiceRecord(svc, set, name, TypePlaceholder)
	return svc
}

// BringUp refuses: a placeholder has no description to run. Starting the
// service by name loads its description, which replaces the placeholder.
func (s *PlaceholderService) BringUp() bool {
	s.services.logger.Error("Service '%s': not loaded (placeholder)", s.serviceName)
	return false
}

// BringDown has nothing to stop.
func (s *PlaceholderService) BringDown() { s.Stopped() }

// CanInterruptStart and InterruptStart: there is never a start to interrupt.
func (s *PlaceholderService) CanInterruptStart() bool { return true }
func (s *PlaceholderService) InterruptStart() bool    { return true }
