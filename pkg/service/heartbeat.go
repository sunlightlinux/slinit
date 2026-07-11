package service

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HeartbeatReporter emits a one-line periodic health summary of the
// supervisor's own state — service counts, restart rate, watchdog
// misses, memory usage. Modelled after the runsvdir rolling-buffer
// self-log idea but adapted to what a modern production operator
// actually cares about (SLI/SLO signals rather than raw stderr
// replay).
//
// Zero steady-state overhead when unconfigured: the reporter is only
// constructed when the operator passes --heartbeat-interval.
type HeartbeatReporter struct {
	services *ServiceSet
	logger   HeartbeatLogger
	interval time.Duration
	// window over which the "restarts/N" number is computed. Keeping
	// this bounded (not "restarts since boot") makes the number
	// actionable — a spike is a signal, a running total is noise.
	window time.Duration
	quit   chan struct{}
	done   chan struct{}
	once   sync.Once
}

// HeartbeatLogger is a narrow subset of the daemon logger the
// reporter needs. Kept minimal so tests can pass a stub without
// depending on the full logging package.
type HeartbeatLogger interface {
	Info(format string, args ...interface{})
}

// NewHeartbeatReporter wires a reporter to a ServiceSet and a
// logger. interval <= 0 selects a 5-minute default; window <= 0
// selects a 1-minute default.
func NewHeartbeatReporter(services *ServiceSet, logger HeartbeatLogger,
	interval, window time.Duration) *HeartbeatReporter {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if window <= 0 {
		window = 1 * time.Minute
	}
	return &HeartbeatReporter{
		services: services,
		logger:   logger,
		interval: interval,
		window:   window,
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Run blocks until Stop() is called. Intended to run in a goroutine.
// Emits the first heartbeat one interval after Run() starts (not
// immediately) — the point is to sample the *steady state*, and a
// heartbeat at t=0 says nothing useful.
func (h *HeartbeatReporter) Run() {
	defer close(h.done)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.quit:
			return
		case <-ticker.C:
			h.emit()
		}
	}
}

// Stop signals the reporter to exit and waits for the goroutine.
func (h *HeartbeatReporter) Stop() {
	h.once.Do(func() {
		close(h.quit)
	})
	<-h.done
}

// emit builds the summary line and writes it via the logger.
func (h *HeartbeatReporter) emit() {
	counts := h.services.CountByState()
	restarts := h.services.RestartsInLast(h.window)
	watchdogs := h.services.WatchdogMisses()
	rss := readOwnRSSKB()

	// Format: "heartbeat: active=N failed=N stopped=N starting=N
	//         stopping=N restarts(1m)=N watchdog-misses=N rss=NkB"
	// Keep it grep-friendly — key=value pairs, no colons in the
	// values, so a naive parser works.
	h.logger.Info(
		"heartbeat: active=%d failed=%d stopped=%d starting=%d stopping=%d restarts(%s)=%d watchdog-misses=%d rss=%dkB",
		counts.Active, counts.Failed, counts.Stopped,
		counts.Starting, counts.Stopping,
		compactDuration(h.window), restarts,
		watchdogs, rss)
}

// compactDuration renders a duration in the shortest human-readable
// form the reporter can produce — 60s → "1m", 300s → "5m", etc.
// Used only for label rendering, not accuracy-critical.
func compactDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return fmt.Sprintf("%ds", int64(d.Seconds()))
	}
}

// readOwnRSSKB parses /proc/self/status and returns the VmRSS value
// in kilobytes. Returns 0 on any error — heartbeat is a best-effort
// signal, not an audit source.
func readOwnRSSKB() uint64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		// "VmRSS:\t   1234 kB"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return v
	}
	return 0
}
