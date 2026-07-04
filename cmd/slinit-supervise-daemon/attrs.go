package main

import (
	"os"
	"strconv"
	"syscall"
)

// Same trio of helpers as slinit-start-stop-daemon's attrs.go. Kept
// duplicated (not shared through internal/) because the two binaries
// otherwise have zero cross-package coupling and the surface is 50
// lines total — the abstraction wouldn't pay for itself.

// setOOMScoreAdj writes /proc/PID/oom_score_adj. Best-effort; caller
// treats a failure as a warning.
func setOOMScoreAdj(pid, adj int) error {
	return os.WriteFile("/proc/"+strconv.Itoa(pid)+"/oom_score_adj",
		[]byte(strconv.Itoa(adj)), 0200)
}

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
