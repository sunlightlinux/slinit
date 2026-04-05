package service

import (
	"sync"
	"time"
)

// StartLimiter controls how many services can be starting concurrently.
// Services that have been starting longer than slowThreshold are considered
// "slow" and no longer count against the limit, allowing other services
// to proceed.
type StartLimiter struct {
	maxConcurrent int
	slowThreshold time.Duration

	mu        sync.Mutex
	starting  map[Service]time.Time // service → when it entered STARTING
	fastCount int                   // cached count of non-slow starters
	waiters   []startWaiter         // services waiting for a slot
}

type startWaiter struct {
	svc Service
	ch  chan struct{} // closed when slot is available
}

// NewStartLimiter creates a limiter with the given max concurrency and
// slow-starter threshold. If max <= 0, no limiting is applied.
func NewStartLimiter(max int, slowThreshold time.Duration) *StartLimiter {
	if slowThreshold <= 0 {
		slowThreshold = 10 * time.Second
	}
	return &StartLimiter{
		maxConcurrent: max,
		slowThreshold: slowThreshold,
		starting:      make(map[Service]time.Time),
	}
}

// Acquire attempts to claim a start slot for the service.
// Returns true if the service may proceed immediately.
// Returns false if the service must wait; in that case, the returned
// channel will be closed when a slot becomes available.
func (sl *StartLimiter) Acquire(svc Service) (bool, <-chan struct{}) {
	if sl.maxConcurrent <= 0 {
		return true, nil
	}

	sl.mu.Lock()
	defer sl.mu.Unlock()

	sl.refreshFastCount()
	if sl.fastCount < sl.maxConcurrent {
		sl.starting[svc] = time.Now()
		sl.fastCount++
		return true, nil
	}

	// No slot available — queue the service
	ch := make(chan struct{}, 1)
	sl.waiters = append(sl.waiters, startWaiter{svc: svc, ch: ch})
	return false, ch
}

// Release is called when a service leaves STARTING (either successfully
// started or failed). It frees the slot and wakes the next waiter.
func (sl *StartLimiter) Release(svc Service) {
	if sl.maxConcurrent <= 0 {
		return
	}

	sl.mu.Lock()
	defer sl.mu.Unlock()

	if _, ok := sl.starting[svc]; ok {
		delete(sl.starting, svc)
		sl.fastCount-- // will be corrected by refreshFastCount in wakeNext
		if sl.fastCount < 0 {
			sl.fastCount = 0
		}
	}
	sl.wakeNext()
}

// CancelWait removes a service from the wait queue (e.g. if it was
// stopped before getting a slot).
func (sl *StartLimiter) CancelWait(svc Service) {
	if sl.maxConcurrent <= 0 {
		return
	}

	sl.mu.Lock()
	defer sl.mu.Unlock()

	for i, w := range sl.waiters {
		if w.svc == svc {
			sl.waiters = append(sl.waiters[:i], sl.waiters[i+1:]...)
			return
		}
	}
}

// ActiveCount returns the number of non-slow starting services.
func (sl *StartLimiter) ActiveCount() int {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	sl.refreshFastCount()
	return sl.fastCount
}

// refreshFastCount recalculates the fast starter count.
// Only iterates the map when services may have crossed the slow threshold.
// Caller must hold sl.mu.
func (sl *StartLimiter) refreshFastCount() {
	now := time.Now()
	count := 0
	for _, startTime := range sl.starting {
		if now.Sub(startTime) < sl.slowThreshold {
			count++
		}
	}
	sl.fastCount = count
}

// wakeNext wakes the next waiting service if a slot is available.
// Caller must hold sl.mu.
func (sl *StartLimiter) wakeNext() {
	for len(sl.waiters) > 0 {
		sl.refreshFastCount()
		if sl.fastCount >= sl.maxConcurrent {
			return
		}

		w := sl.waiters[0]
		sl.waiters = sl.waiters[1:]
		sl.starting[w.svc] = time.Now()
		sl.fastCount++
		close(w.ch) // signal the waiter
	}
}

// TotalStarting returns the total number of services in the starting map
// (both slow and non-slow).
func (sl *StartLimiter) TotalStarting() int {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	return len(sl.starting)
}

// WaiterCount returns the number of services waiting for a slot.
func (sl *StartLimiter) WaiterCount() int {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	return len(sl.waiters)
}
