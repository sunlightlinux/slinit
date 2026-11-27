package main

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// setDaemonAffinity applies set to every thread of slinit (--cpu-affinity).
//
// sched_setaffinity acts on one thread, and pid 0 means the calling one:
// that pinned one thread of ~40 — the reserve made at start-up (see
// threadreserve.go) kept the full mask, and so did every thread they
// later created, since a new thread copies its creator's mask. A child
// copies the mask of the thread that forked it, so commands run from
// those threads ran on any CPU too.
//
// The runtime can make a thread while this runs, from a thread not yet
// pinned. So it goes over /proc/self/task until a pass finds no thread
// it has not already pinned: every thread made after that pass began was
// made by a pinned one.
func setDaemonAffinity(set *unix.CPUSet) error {
	done := make(map[int]bool)
	for {
		tasks, err := os.ReadDir("/proc/self/task")
		if err != nil {
			return err
		}
		fresh := false
		for _, t := range tasks {
			tid, err := strconv.Atoi(t.Name())
			if err != nil || done[tid] {
				continue
			}
			fresh = true
			if err := unix.SchedSetaffinity(tid, set); err != nil && err != unix.ESRCH {
				return fmt.Errorf("thread %d: %w", tid, err)
			}
			done[tid] = true
		}
		if !fresh {
			return nil
		}
	}
}
