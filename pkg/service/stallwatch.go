package service

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// schedLock is queueMu plus a count of how many times it has been taken
// for writing.
//
// The count exists because a failed TryRLock does not mean what it looks
// like it means. The state machine takes this lock for every transition,
// so during a busy boot a probe can lose every race it tries without
// anything being wrong — the first version of the watchdog reported a
// stall against a lock that was merely busy, and its own test caught it.
// What a stall actually is: the lock not changing hands. One atomic add
// per acquisition is what distinguishes the two.
type schedLock struct {
	sync.RWMutex
	turns atomic.Uint64
}

// Lock overrides the embedded RWMutex's so every acquisition is counted.
// RLock, RUnlock, Unlock and TryRLock come from the embedded type.
func (l *schedLock) Lock() {
	l.RWMutex.Lock()
	l.turns.Add(1)
}

// Turns is how many times the lock has been taken for writing.
func (l *schedLock) Turns() uint64 { return l.turns.Load() }

// Stall detection for the scheduling lock.
//
// Every state transition in slinit happens with queueMu held, so a
// transition that never finishes takes PID 1 with it: no service starts
// or stops, and every control connection that needs the lock blocks. The
// observable result is a system that is up, idle, and answers nothing —
// which is exactly how the path-activation stall presented, and after
// twelve minutes of it there was nothing to look at but an empty console.
//
// This does not prevent a stall. It makes one say so, and say where. A
// probe takes the lock for reading every few seconds; when it cannot get
// it for long enough that no legitimate hold explains it, PID 1 writes
// every goroutine's stack to a file and names the file on stderr.
//
// The file comes first on purpose. If the thing that is stuck is a write
// to the console — a real possibility, since the state machine logs under
// this same lock — then the stderr notice is lost too, and the file is
// the only thing that survives to be read after a reboot.

const (
	// How often to probe. Cheap: a read lock taken and dropped.
	stallProbeInterval = 5 * time.Second

	// How long the lock must stay unavailable before saying so. Well
	// above any legitimate hold — the longest is a reload, which holds it
	// across reading a service description from disk — and well below the
	// twelve minutes the one observed stall ran for.
	stallThreshold = 30 * time.Second

	// StallDumpPath is where the goroutine dump is written.
	StallDumpPath = "/run/slinit-stall.stack"
)

// WatchForStalls starts the stall probe and returns a function that stops
// it. Intended for PID 1 (and container mode, which is also PID 1); unit
// tests do not want a background goroutine, which is why this is started
// explicitly rather than from NewServiceSet.
func (ss *ServiceSet) WatchForStalls(dumpPath string) func() {
	if dumpPath == "" {
		dumpPath = StallDumpPath
	}
	// Said once at boot so an operator reading the log knows the facility
	// is there and what its threshold is. Without this line, a silent
	// watchdog and an absent one look the same.
	ss.logger.Info("Stall watchdog armed: reports a scheduling lock held longer than %s, with stacks in %s",
		stallThreshold, dumpPath)
	return ss.watchForStalls(stallProbeInterval, stallThreshold, func(held time.Duration) {
		ss.reportStall(held, dumpPath)
	})
}

// watchForStalls is the loop, with the timings and the reporting injected
// so a test can drive it in milliseconds.
func (ss *ServiceSet) watchForStalls(probe, threshold time.Duration, report func(time.Duration)) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(probe)
		defer t.Stop()

		// windowStart is when the current owner took the lock, as far as
		// the probe can tell, and windowTurns is the acquisition count at
		// that moment. A failed probe only counts towards a stall while
		// the count stays put: if it has moved, the lock is changing
		// hands and the system is busy, not wedged.
		var windowStart time.Time
		var windowTurns uint64
		var reported time.Duration

		for {
			select {
			case <-done:
				return
			case <-t.C:
			}

			turns := ss.queueMu.Turns()

			if ss.queueMu.TryRLock() {
				ss.queueMu.RUnlock()
				if reported > 0 {
					ss.logger.Error("slinit: the scheduling lock is free again after %s; "+
						"whatever held it has finished", reported.Round(time.Second))
				}
				windowStart = time.Time{}
				reported = 0
				continue
			}

			if windowStart.IsZero() || turns != windowTurns {
				// Either the first failed probe, or the lock has changed
				// hands since the last one. Start the window here.
				if reported > 0 {
					ss.logger.Error("slinit: the scheduling lock changed hands after %s; "+
						"it was busy, not wedged", reported.Round(time.Second))
				}
				windowStart = time.Now()
				windowTurns = turns
				reported = 0
				continue
			}
			held := time.Since(windowStart)
			if held < threshold {
				continue
			}
			// Report once, then at widening intervals, so a permanent
			// wedge leaves evidence that it was still wedged later
			// without filling the log.
			if reported > 0 && held < reported*4 {
				continue
			}
			reported = held
			report(held)
		}
	}()
	return func() { close(done) }
}

// reportStall writes every goroutine's stack to dumpPath and names it on
// stderr. Holds no lock: it is only called when the probe failed, so
// there is nothing to release.
func (ss *ServiceSet) reportStall(held time.Duration, dumpPath string) {
	msg := fmt.Sprintf("slinit: the scheduling lock has not been free for %s. "+
		"No service can start or stop and control connections that need it will "+
		"block. This is either a transition that cannot finish or an unusually "+
		"long legitimate hold", held.Round(time.Second))

	stacks := goroutineDump()

	// File first — see the note at the top of this file.
	var wrote string
	if dumpPath != "" {
		body := fmt.Sprintf("%s\nheld-for: %s\ntime: %s\n\n%s",
			msg, held, time.Now().Format(time.RFC3339), stacks)
		if err := os.WriteFile(dumpPath, []byte(body), 0600); err == nil {
			wrote = dumpPath
		}
	}

	if wrote != "" {
		ss.logger.Error("%s. Every goroutine's stack is in %s", msg, wrote)
	} else {
		// Nowhere to put it (no writable /run yet, read-only root), so
		// the stacks go to stderr even though they are long: a stall with
		// no stacks is the thing being fixed here.
		ss.logger.Error("%s. Could not write %s, so the stacks follow:\n%s",
			msg, dumpPath, stacks)
	}
}

// goroutineDump returns the stacks of every goroutine, growing the buffer
// until it fits. runtime.Stack with all=true stops the world for the
// duration of the copy; at a 30-second threshold that is a trade worth
// making.
func goroutineDump() string {
	size := 64 * 1024
	for {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n < size {
			return string(buf[:n])
		}
		if size >= 8*1024*1024 {
			return string(buf[:n]) + "\n[truncated]\n"
		}
		size *= 2
	}
}
