package service

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// defaultOOMPollInterval is how often the watcher samples
// <cgroup>/memory.events for oom_kill / oom_group_kill counter
// increments. Cheap (one short read) and 1 s gives a sensible
// trade-off between latency and wakeups. Tests override via
// setOOMPollIntervalForTesting.
var oomPollInterval = time.Second

// oomWatcher polls the service's cgroup v2 memory.events file and
// applies the configured oom-policy when an OOM kill is reported.
// One per service while STARTED; cancelled in Stopped().
type oomWatcher struct {
	stop chan struct{}
}

// armOOMWatcher starts a goroutine that polls the service's cgroup
// memory.events file. No-op if the policy is OOMContinue (nothing to
// react to) or if no cgroup path is configured (no cgroup → no v2
// memory accounting → no events to read).
func (sr *ServiceRecord) armOOMWatcher() {
	sr.cancelOOMWatcher()
	if sr.oomPolicy == OOMContinue {
		return
	}
	cgPath := sr.EffectiveCgroupPath()
	if cgPath == "" {
		return
	}
	stop := make(chan struct{})
	sr.oomWatch = &oomWatcher{stop: stop}

	policy := sr.oomPolicy
	svc := sr.self
	set := sr.services
	name := sr.serviceName
	eventsPath := cgPath + "/memory.events"

	// Read the baseline so we only act on increments from now on.
	// A read failure here (cgroup not yet populated, missing file)
	// is non-fatal; we re-read on each tick.
	baseline := readOOMCounters(eventsPath)

	go func() {
		ticker := time.NewTicker(oomPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			cur := readOOMCounters(eventsPath)
			if cur.oomKill <= baseline.oomKill && cur.oomGroupKill <= baseline.oomGroupKill {
				continue
			}
			set.queueMu.Lock()
			// Re-check state under lock: the service may have stopped
			// between the event fire and us acquiring the lock.
			if svc.State() != StateStarted {
				set.queueMu.Unlock()
				return
			}
			set.logger.Info(
				"Service '%s': cgroup OOM kill observed (oom_kill=%d→%d), applying policy=%s",
				name, baseline.oomKill, cur.oomKill, policy)
			switch policy {
			case OOMStop:
				svc.Record().Stop(true)
				set.processQueuesLocked()
			case OOMKill:
				_ = process.KillCgroup(cgPath, syscall.SIGKILL)
				// The subsequent ChildExit (group leader killed)
				// drives the service through its normal failure path;
				// no further scheduling work is needed here.
			}
			set.queueMu.Unlock()
			return
		}
	}()
}

// cancelOOMWatcher stops the watcher goroutine if armed.
func (sr *ServiceRecord) cancelOOMWatcher() {
	if sr.oomWatch != nil {
		close(sr.oomWatch.stop)
		sr.oomWatch = nil
	}
}

type oomCounters struct {
	oomKill      uint64
	oomGroupKill uint64
}

// readOOMCounters parses the kernel-supplied key=value list in
// memory.events. Returns zeroed counters when the file is missing or
// malformed — the watcher treats that as "no event yet".
func readOOMCounters(path string) oomCounters {
	f, err := os.Open(path)
	if err != nil {
		return oomCounters{}
	}
	defer f.Close()
	var out oomCounters
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "oom_kill":
			out.oomKill = v
		case "oom_group_kill":
			out.oomGroupKill = v
		}
	}
	return out
}

// setOOMPollIntervalForTesting overrides the watcher's poll interval.
// Restores the previous value via the returned cleanup func.
func setOOMPollIntervalForTesting(d time.Duration) func() {
	prev := oomPollInterval
	oomPollInterval = d
	return func() { oomPollInterval = prev }
}
