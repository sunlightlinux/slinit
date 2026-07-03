package main

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Scheduler policy numbers per <linux/sched.h>. SCHED_DEADLINE (6) is
// deliberately not exposed via a name here — it needs runtime/deadline
// tunings that OpenRC does not have a flag for.
const (
	schedOther = 0
	schedFIFO  = 1
	schedRR    = 2
	schedBatch = 3
	schedIdle  = 5
)

// parseSchedPolicy maps an OpenRC-style scheduler name to a policy number.
func parseSchedPolicy(s string) (uint32, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "other", "normal":
		return schedOther, nil
	case "fifo":
		return schedFIFO, nil
	case "rr":
		return schedRR, nil
	case "batch":
		return schedBatch, nil
	case "idle":
		return schedIdle, nil
	}
	return 0, fmt.Errorf("unknown scheduler %q (want other|fifo|rr|batch|idle)", s)
}

// applySched programs a policy + priority on pid via sched_setattr(2).
// FIFO/RR require CAP_SYS_NICE or a sufficient RLIMIT_RTPRIO; kernel
// errors are surfaced to the caller.
func applySched(pid int, policy, priority uint32) error {
	attr := unix.SchedAttr{
		Size:     uint32(unsafe.Sizeof(unix.SchedAttr{})),
		Policy:   policy,
		Priority: priority,
	}
	return unix.SchedSetAttr(pid, &attr, 0)
}
