package service

import (
	"math"
	"time"
)

// expBackoffDelay is the delay before the n-th automatic restart (n from
// 1) under restart-steps, as systemd's RestartSteps= computes it
// (service_restart_usec_nth): from base at the first restart, growing
// geometrically to max over steps restarts, and max after that,
//
//	delay(n) = base * (max/base)^((n-1)/steps)
//
// It is base whenever the backoff cannot apply: no steps, a zero base
// (a geometric series cannot leave 0), or a max not above base.
func expBackoffDelay(base, max time.Duration, steps, n int) time.Duration {
	if steps <= 0 || base <= 0 || max <= base || n <= 1 {
		return base
	}
	if n > steps {
		return max
	}
	f := math.Pow(float64(max)/float64(base), float64(n-1)/float64(steps))
	return time.Duration(math.Round(float64(base) * f))
}
