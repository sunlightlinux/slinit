package main

import "time"

// respawnLimiter tracks crash timestamps in a rolling window so the
// supervisor can decide whether to keep respawning or give up. Matches
// OpenRC's semantic: if the daemon dies more than max times within a
// period-second window, the supervisor exits.
type respawnLimiter struct {
	max    int // 0 = unlimited
	period time.Duration
	times  []time.Time
	count  int // for logging
}

func newRespawnLimiter(max int, period time.Duration) *respawnLimiter {
	return &respawnLimiter{max: max, period: period}
}

// allowRespawn records a crash at now and returns true if the
// supervisor should try again. When max is 0 (unlimited) it always
// returns true.
func (r *respawnLimiter) allowRespawn(now time.Time) bool {
	if r.max <= 0 {
		return true
	}
	// Drop entries older than the window.
	cutoff := now.Add(-r.period)
	kept := r.times[:0]
	for _, t := range r.times {
		if !t.Before(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	r.times = kept
	r.count = len(kept)
	// max is treated as "more than" — matches OpenRC's --respawn-max
	// docs: N crashes within period is OK; N+1 gives up.
	return len(kept) <= r.max
}

// backoffDelay returns the sleep before the next respawn. Combines the
// fixed --respawn-delay with the stepped --respawn-delay-step, capped
// at --respawn-delay-cap.
func backoffDelay(opts Options, respawn int) time.Duration {
	base := opts.RespawnDelay
	if opts.RespawnDelayStep <= 0 {
		return base
	}
	// Step is added respawn times so the first respawn gets one step,
	// the second gets two, etc. Cap prevents runaway growth.
	extra := time.Duration(respawn) * opts.RespawnDelayStep
	total := base + extra
	if opts.RespawnDelayCap > 0 && total > opts.RespawnDelayCap {
		total = opts.RespawnDelayCap
	}
	return total
}
