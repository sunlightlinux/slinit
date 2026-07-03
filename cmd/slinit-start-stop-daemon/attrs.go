package main

import (
	"os"
	"strconv"
	"syscall"
)

// setOOMScoreAdj writes /proc/PID/oom_score_adj. Best-effort — caller
// treats a failure as a warning, not a fatal.
func setOOMScoreAdj(pid, adj int) error {
	return os.WriteFile("/proc/"+strconv.Itoa(pid)+"/oom_score_adj",
		[]byte(strconv.Itoa(adj)), 0200)
}

// setIOPrio invokes the ioprio_set syscall for a given PID/class/level.
// class: 1=RT, 2=BE, 3=IDLE. Level: 0-7 (ignored for IDLE).
const (
	sysIoprioSet     = 251
	ioprioWhoProcess = 1
	ioprioClassShift = 13
)

func setIOPrio(pid, class, level int) error {
	prio := uintptr((class << ioprioClassShift) | level)
	_, _, errno := syscall.Syscall(sysIoprioSet,
		uintptr(ioprioWhoProcess), uintptr(pid), prio)
	if errno != 0 {
		return errno
	}
	return nil
}

// parseIOSchedClass maps names to ioprio_set class numbers.
func parseIOSchedClass(name string) (int, bool) {
	switch name {
	case "realtime", "rt", "1":
		return 1, true
	case "best-effort", "be", "2":
		return 2, true
	case "idle", "3":
		return 3, true
	}
	return 0, false
}

