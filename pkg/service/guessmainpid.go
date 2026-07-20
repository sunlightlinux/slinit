package service

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// guessMainPIDFromCgroup implements systemd GuessMainPID= for
// bgprocess services that lack a pid-file. Reads cgroup.procs and
// returns the first non-slinit pid. "non-slinit" here means "not our
// own pid" — the launcher already exited by the time this runs, so
// the only remaining processes in the cgroup are the daemon and any
// grandchildren it spawned. We pick the numerically-lowest pid,
// which is systemd's convention (approximates "the earliest fork
// still alive" without racing on /proc timestamps).
//
// Returns an error when the cgroup is empty, missing, or the file
// can't be parsed — the caller treats that as PIDResultFailed.
func guessMainPIDFromCgroup(cgPath string) (int, error) {
	if cgPath == "" {
		return 0, fmt.Errorf("guess-main-pid: no delegated cgroup — set cgroup = or slice = for this service")
	}
	data, err := os.ReadFile(cgPath + "/cgroup.procs")
	if err != nil {
		return 0, fmt.Errorf("guess-main-pid: read cgroup.procs: %w", err)
	}
	self := os.Getpid()
	best := -1
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n, perr := strconv.Atoi(line)
		if perr != nil || n <= 0 || n == self {
			continue
		}
		if best == -1 || n < best {
			best = n
		}
	}
	if best == -1 {
		return 0, fmt.Errorf("guess-main-pid: cgroup.procs contained no candidate pid")
	}
	return best, nil
}
