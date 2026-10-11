package rusage

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// userHZ is the unit of the time fields in /proc/<pid>/stat. It is 100
// on Linux as a userspace ABI constant, independent of CONFIG_HZ, so it
// is written here rather than read through sysconf (which would need
// cgo).
const userHZ = 100

// Live reports the usage of one running process from /proc. This is the
// per-service adaptation of launchd's getrusage, which has no such form:
// getrusage can only report a process's own usage or that of children it
// has already reaped, and slinit is neither of those for a service that
// is still running.
//
// What it covers, precisely: this one process. Not the processes it
// forked. A forking daemon's workers, or a scripted service's
// grandchildren, are outside the numbers — use the service's cgroup
// accounting for a subtree total. The kernel maintains no block-I/O
// counters per live task either, so those two fields are reported
// unavailable rather than as zero.
func Live(scope string, pid int) (*Snapshot, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("no running process")
	}

	statRaw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, fmt.Errorf("reading /proc/%d/stat: %w", pid, err)
	}
	minflt, majflt, utime, stime, err := parseStat(string(statRaw))
	if err != nil {
		return nil, err
	}

	s := &Snapshot{
		Scope:    scope,
		Source:   fmt.Sprintf("/proc/%d (live, this process only)", pid),
		UserTime: float64(utime) / userHZ,
		SysTime:  float64(stime) / userHZ,
		MinFlt:   minflt,
		MajFlt:   majflt,
		// The kernel exposes no per-task equivalent of ru_inblock /
		// ru_oublock. Saying so beats two zeroes that read as "this
		// process did no I/O".
		Partial: []string{"BlockInputOps", "BlockOutputOps"},
	}

	// /proc/<pid>/status carries the peak RSS and the context-switch
	// counters. A process that exits between the two reads leaves the
	// snapshot without them rather than failing the whole request.
	if statusRaw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		hwm, vol, invol, have := parseStatus(string(statusRaw))
		s.MaxRSSKiB = hwm
		s.VolCSw = vol
		s.InvolCSw = invol
		for _, f := range []struct {
			name string
			ok   bool
		}{
			{"MaxRSSKiB", have["hwm"]},
			{"VoluntaryCtxSwitches", have["vol"]},
			{"InvoluntaryCtxSwitches", have["invol"]},
		} {
			if !f.ok {
				s.Partial = append(s.Partial, f.name)
			}
		}
	} else {
		s.Partial = append(s.Partial,
			"MaxRSSKiB", "VoluntaryCtxSwitches", "InvoluntaryCtxSwitches")
	}

	return s, nil
}

// parseStat pulls minflt, majflt, utime and stime out of a
// /proc/<pid>/stat line.
//
// The fields are counted from after the LAST ')', not by splitting the
// whole line: field 2 is the executable name in parentheses and may
// itself contain spaces and parentheses, so a process called "a b)c"
// shifts every later field for anyone who splits naively.
func parseStat(raw string) (minflt, majflt, utime, stime int64, err error) {
	close := strings.LastIndex(raw, ")")
	if close < 0 || close+2 > len(raw) {
		return 0, 0, 0, 0, fmt.Errorf("malformed /proc stat line")
	}
	// After the name, fields start at state (stat field 3), so index 0
	// here is field 3 and field N is at index N-3.
	f := strings.Fields(raw[close+1:])
	get := func(statField int) (int64, error) {
		i := statField - 3
		if i < 0 || i >= len(f) {
			return 0, fmt.Errorf("/proc stat has no field %d", statField)
		}
		return strconv.ParseInt(f[i], 10, 64)
	}
	if minflt, err = get(10); err != nil {
		return
	}
	if majflt, err = get(12); err != nil {
		return
	}
	if utime, err = get(14); err != nil {
		return
	}
	stime, err = get(15)
	return
}

// parseStatus reads VmHWM and the context-switch counters, reporting
// which ones were present. VmHWM is absent for a kernel thread, so its
// absence is a fact about the process, not a parse failure.
func parseStatus(raw string) (hwm, vol, invol int64, have map[string]bool) {
	have = map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		// The separator is a TAB and the value may carry a " kB" suffix.
		fields := strings.Fields(val)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "VmHWM":
			hwm, have["hwm"] = n, true
		case "voluntary_ctxt_switches":
			vol, have["vol"] = n, true
		case "nonvoluntary_ctxt_switches":
			invol, have["invol"] = n, true
		}
	}
	return hwm, vol, invol, have
}
