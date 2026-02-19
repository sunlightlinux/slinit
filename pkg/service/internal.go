package service

// InternalService is a service that has no external process.
// It transitions to STARTED immediately when brought up.
type InternalService struct {
	ServiceRecord
}

// NewInternalService creates a new internal service.
func NewInternalService(set *ServiceSet, name string) *InternalService {
	svc := &InternalService{}
	svc.ServiceRecord = *NewServiceRecord(svc, set, name, TypeInternal)
	return svc
}

// BringUp for an internal service just marks it as started immediately.
func (s *InternalService) BringUp() bool {
	s.Started()
	return true
}

// BringDown for an internal service calls stopped immediately.
func (s *InternalService) BringDown() {
	s.Stopped()
}

// CanInterruptStart returns true since internal services start instantly.
func (s *InternalService) CanInterruptStart() bool {
	return true
}

// InterruptStart cancels the start immediately.
func (s *InternalService) InterruptStart() bool {
	return true
}
