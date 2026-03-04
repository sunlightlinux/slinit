package eventloop

import "time"

// ServiceTimer wraps a time.Timer for use by service types.
// Each service has at most one active timer (multipurpose, like dinit's
// process_restart_timer).
type ServiceTimer struct {
	timer *time.Timer
	armed bool
}

// NewServiceTimer creates a new (disarmed) timer.
func NewServiceTimer() *ServiceTimer {
	return &ServiceTimer{}
}

// Arm starts the timer with the given duration.
// If already armed, it is stopped and re-armed.
func (t *ServiceTimer) Arm(d time.Duration) {
	t.Stop()
	t.timer = time.NewTimer(d)
	t.armed = true
}

// Stop disarms the timer and drains any pending fire so that a
// subsequent select on Chan() never receives a stale event.
func (t *ServiceTimer) Stop() {
	if t.timer != nil {
		if !t.timer.Stop() {
			// Timer already fired; drain the channel to prevent a stale read.
			select {
			case <-t.timer.C:
			default:
			}
		}
		t.timer = nil
	}
	t.armed = false
}

// IsArmed returns true if the timer is currently armed.
func (t *ServiceTimer) IsArmed() bool {
	return t.armed
}

// Chan returns the timer channel, or nil if not armed.
func (t *ServiceTimer) Chan() <-chan time.Time {
	if t.timer != nil {
		return t.timer.C
	}
	return nil
}
