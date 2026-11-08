package service

import "time"

// InternalService is a service that has no external process.
// It transitions to STARTED immediately when brought up. It can carry a cron
// task, for a periodic job with nothing to supervise in between: as a process
// service that took a placeholder such as `sleep infinity`.
type InternalService struct {
	ServiceRecord
	cronSlot
}

// SetCronConfig configures the cron task in interval mode.
func (s *InternalService) SetCronConfig(cmd []string, interval, delay time.Duration, onError string) {
	s.setCronRunner(NewCronRunner(s, cmd, interval, delay, onError, s.services.logger))
}

// SetCronCalendar configures the cron task in calendar mode.
func (s *InternalService) SetCronCalendar(
	cmd []string, calendar *CalendarSpec,
	randomizedDelay time.Duration, persistent bool, onError string,
) {
	s.setCronRunner(NewCalendarCronRunner(
		s, cmd, calendar, randomizedDelay, persistent, onError, s.services.logger))
}

// NewInternalService creates a new internal service.
func NewInternalService(set *ServiceSet, name string) *InternalService {
	svc := &InternalService{}
	svc.ServiceRecord = *NewServiceRecord(svc, set, name, TypeInternal)
	return svc
}

// BringUp for an internal service just marks it as started immediately.
func (s *InternalService) BringUp() bool {
	// condition-*/assert-* apply here too, as systemd's conditions do on
	// a target: a false condition skips the milestone (STARTED, marked
	// skipped, so dependents proceed), a false assert fails it. Until
	// 3.7.0 only process, scripted and bgprocess evaluated them, and on
	// an internal service they were ignored without a word.
	switch outcome, reason := s.CheckPredicates(); outcome {
	case PredFailed:
		s.services.logger.Error("Service '%s': %s", s.serviceName, reason)
		return false
	case PredSkip:
		s.services.logger.Info("Service '%s': skipped (%s)", s.serviceName, reason)
		s.markSkippedStartReason(reason)
		return true
	}
	s.Started()
	if s.cronRunner != nil {
		s.services.logger.Info("Service '%s': starting cron task (%s)", s.serviceName, s.cronRunner.Schedule(time.Now()))
		s.cronRunner.Start()
	}
	return true
}

// BringDown for an internal service calls stopped immediately.
func (s *InternalService) BringDown() {
	if s.cronRunner != nil {
		s.cronRunner.Stop()
	}
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
