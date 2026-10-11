// Package rusage reports resource usage for slinit itself, for all the
// children it has reaped, and for a single running service. It is
// slinit's answer to launchd's `launchctl getrusage`.
//
// launchd prints all 16 fields of struct rusage. Seven of them are
// documented "unmaintained" on Linux and are always zero: ru_ixrss,
// ru_idrss, ru_isrss, ru_nswap, ru_msgsnd, ru_msgrcv and ru_nsignals.
// This package omits them and names them once, because printing a zero
// next to "swaps" or "signals received" presents seven numbers that were
// never measured — and a reader has no way to tell them from a real
// zero.
package rusage

import (
	"fmt"
	"sort"
	"strings"
	"syscall"
)

// Unmaintained lists the struct rusage fields the Linux kernel does not
// fill, in launchd's own wording, so the report can say what it is not
// telling you rather than quietly dropping them.
var Unmaintained = []string{
	"shared text memory size (ru_ixrss)",
	"unshared data size (ru_idrss)",
	"unshared stack size (ru_isrss)",
	"swaps (ru_nswap)",
	"messages sent (ru_msgsnd)",
	"messages received (ru_msgrcv)",
	"signals received (ru_nsignals)",
}

// Snapshot holds the fields Linux actually maintains. Times are seconds;
// MaxRSSKiB is kibibytes, as the kernel reports ru_maxrss.
type Snapshot struct {
	Scope     string // "self", "children" or a service name
	Source    string // where the numbers came from
	UserTime  float64
	SysTime   float64
	MaxRSSKiB int64
	MinFlt    int64
	MajFlt    int64
	InBlock   int64
	OuBlock   int64
	VolCSw    int64
	InvolCSw  int64

	// Partial names the maintained fields this source could not supply,
	// which is not the same thing as a zero. A live /proc read cannot
	// see block I/O counts, for instance.
	Partial []string
}

// FromSyscall converts a kernel struct rusage.
func FromSyscall(scope, source string, ru *syscall.Rusage) *Snapshot {
	return &Snapshot{
		Scope:     scope,
		Source:    source,
		UserTime:  tvSeconds(ru.Utime),
		SysTime:   tvSeconds(ru.Stime),
		MaxRSSKiB: int64(ru.Maxrss),
		MinFlt:    int64(ru.Minflt),
		MajFlt:    int64(ru.Majflt),
		InBlock:   int64(ru.Inblock),
		OuBlock:   int64(ru.Oublock),
		VolCSw:    int64(ru.Nvcsw),
		InvolCSw:  int64(ru.Nivcsw),
	}
}

func tvSeconds(tv syscall.Timeval) float64 {
	return float64(tv.Sec) + float64(tv.Usec)/1e6
}

// Self reports slinit's own usage: launchd's `getrusage self`.
func Self() (*Snapshot, error) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return nil, fmt.Errorf("getrusage(RUSAGE_SELF): %w", err)
	}
	return FromSyscall("self", "getrusage(RUSAGE_SELF)", &ru), nil
}

// Children reports the aggregate of every child slinit has reaped and
// waited for: launchd's `getrusage children`.
//
// Read what this means in an init system before trusting it. As PID 1,
// slinit reaps orphans from the whole system, so the total covers
// processes it never started and cannot name. It also only counts
// children already waited for, so a service running right now
// contributes nothing to it. For one service's usage, ask for that
// service.
func Children() (*Snapshot, error) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_CHILDREN, &ru); err != nil {
		return nil, fmt.Errorf("getrusage(RUSAGE_CHILDREN): %w", err)
	}
	return FromSyscall("children", "getrusage(RUSAGE_CHILDREN)", &ru), nil
}

// Render formats a snapshot as the `Key=Value` body the control
// protocol carries and slinitctl prints. Keys are stable; new ones
// append, so a caller greps for what it knows.
func Render(s *Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Scope=%s\n", s.Scope)
	fmt.Fprintf(&b, "Source=%s\n", s.Source)
	fmt.Fprintf(&b, "UserTimeSec=%.6f\n", s.UserTime)
	fmt.Fprintf(&b, "SystemTimeSec=%.6f\n", s.SysTime)
	fmt.Fprintf(&b, "MaxRSSKiB=%d\n", s.MaxRSSKiB)
	fmt.Fprintf(&b, "PageReclaims=%d\n", s.MinFlt)
	fmt.Fprintf(&b, "PageFaults=%d\n", s.MajFlt)
	fmt.Fprintf(&b, "BlockInputOps=%d\n", s.InBlock)
	fmt.Fprintf(&b, "BlockOutputOps=%d\n", s.OuBlock)
	fmt.Fprintf(&b, "VoluntaryCtxSwitches=%d\n", s.VolCSw)
	fmt.Fprintf(&b, "InvoluntaryCtxSwitches=%d\n", s.InvolCSw)
	if len(s.Partial) > 0 {
		p := append([]string(nil), s.Partial...)
		sort.Strings(p)
		fmt.Fprintf(&b, "Unavailable=%s\n", strings.Join(p, ","))
	}
	return b.String()
}
