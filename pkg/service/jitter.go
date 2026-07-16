package service

import (
	"math/rand/v2"
	"time"
)

// jitter returns a random duration in the half-open interval [0, max). A
// non-positive max returns zero — the caller is responsible for the
// contract that jitter is always additive to a base delay.
//
// Uses the top-level math/rand/v2 API which is safe for concurrent
// callers and seeded automatically from the runtime. No goroutine
// synchronisation is needed here — the daemon calls this from the
// service-set queueMu path.
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max)))
}
