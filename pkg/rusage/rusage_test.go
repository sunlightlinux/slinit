package rusage

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestFromSyscallConvertsTimesAndFields(t *testing.T) {
	ru := &syscall.Rusage{
		Utime:   syscall.Timeval{Sec: 2, Usec: 500000},
		Stime:   syscall.Timeval{Sec: 0, Usec: 250000},
		Maxrss:  4096,
		Minflt:  11,
		Majflt:  2,
		Inblock: 7,
		Oublock: 9,
		Nvcsw:   13,
		Nivcsw:  5,
	}
	s := FromSyscall("self", "test", ru)
	if s.UserTime != 2.5 {
		t.Errorf("UserTime = %v, want 2.5", s.UserTime)
	}
	if s.SysTime != 0.25 {
		t.Errorf("SysTime = %v, want 0.25", s.SysTime)
	}
	for _, c := range []struct {
		name string
		got  int64
		want int64
	}{
		{"MaxRSSKiB", s.MaxRSSKiB, 4096},
		{"MinFlt", s.MinFlt, 11},
		{"MajFlt", s.MajFlt, 2},
		{"InBlock", s.InBlock, 7},
		{"OuBlock", s.OuBlock, 9},
		{"VolCSw", s.VolCSw, 13},
		{"InvolCSw", s.InvolCSw, 5},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestRenderNeverPrintsUnmaintainedFields(t *testing.T) {
	// The point of this port: the seven fields Linux does not fill must
	// not appear as zeros. launchd prints them; we must not.
	body := Render(FromSyscall("self", "test", &syscall.Rusage{}))
	for _, forbidden := range []string{
		"Swaps", "MessagesSent", "MessagesReceived", "SignalsReceived",
		"SharedTextMemory", "UnsharedData", "UnsharedStack",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Render() emitted %q, a field Linux never maintains", forbidden)
		}
	}
}

func TestRenderListsPartialFields(t *testing.T) {
	s := FromSyscall("svc", "test", &syscall.Rusage{})
	s.Partial = []string{"BlockOutputOps", "BlockInputOps"}
	body := Render(s)
	// Sorted, so the line is stable for a differ.
	if !strings.Contains(body, "Unavailable=BlockInputOps,BlockOutputOps") {
		t.Errorf("Render() = %q, want a sorted Unavailable line", body)
	}
}

func TestRenderOmitsUnavailableWhenComplete(t *testing.T) {
	body := Render(FromSyscall("self", "test", &syscall.Rusage{}))
	if strings.Contains(body, "Unavailable=") {
		t.Errorf("Render() emitted an empty Unavailable line: %q", body)
	}
}

func TestSelfReportsNonZeroUserTime(t *testing.T) {
	// A test binary has necessarily burnt some CPU by now, so a zero
	// here means the syscall result was dropped on the floor.
	s, err := Self()
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if s.UserTime+s.SysTime <= 0 {
		t.Errorf("Self() reported no CPU time at all (%v user, %v sys)", s.UserTime, s.SysTime)
	}
	if s.MaxRSSKiB <= 0 {
		t.Errorf("Self() reported MaxRSSKiB = %d", s.MaxRSSKiB)
	}
	if s.Scope != "self" {
		t.Errorf("Scope = %q", s.Scope)
	}
}

func TestChildrenDoesNotError(t *testing.T) {
	s, err := Children()
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	// The values may legitimately be zero (no children reaped in this
	// process), so the only assertion available is the scope label.
	if s.Scope != "children" {
		t.Errorf("Scope = %q, want children", s.Scope)
	}
}

func TestParseStatSurvivesParensInProcessName(t *testing.T) {
	// The trap this parser exists for: comm is in parentheses and may
	// contain spaces and parens itself. Splitting the whole line shifts
	// every later field. Fields: 3=state … 10=minflt, 12=majflt,
	// 14=utime, 15=stime.
	tail := "R 1 1 1 0 -1 0 " + // 3..9
		"111 0 222 0 " + // 10=minflt 11 12=majflt 13
		"333 444" // 14=utime 15=stime
	for _, comm := range []string{"(bash)", "(weird name)", "(a) b)c)", "(has (nested) parens)"} {
		raw := "42 " + comm + " " + tail
		minflt, majflt, utime, stime, err := parseStat(raw)
		if err != nil {
			t.Errorf("parseStat(%q) = %v", comm, err)
			continue
		}
		if minflt != 111 || majflt != 222 || utime != 333 || stime != 444 {
			t.Errorf("parseStat(%q) = %d,%d,%d,%d; want 111,222,333,444",
				comm, minflt, majflt, utime, stime)
		}
	}
}

func TestParseStatRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "no parens here", "42 (x)", "42 (x) R"} {
		if _, _, _, _, err := parseStat(raw); err == nil {
			t.Errorf("parseStat(%q) succeeded, want an error", raw)
		}
	}
}

func TestParseStatusReadsTheThreeFields(t *testing.T) {
	raw := "Name:\tsh\nVmHWM:\t  7688 kB\n" +
		"voluntary_ctxt_switches:\t42\nnonvoluntary_ctxt_switches:\t7\n"
	hwm, vol, invol, have := parseStatus(raw)
	if hwm != 7688 || vol != 42 || invol != 7 {
		t.Errorf("parseStatus = %d,%d,%d; want 7688,42,7", hwm, vol, invol)
	}
	for _, k := range []string{"hwm", "vol", "invol"} {
		if !have[k] {
			t.Errorf("parseStatus did not report %q as present", k)
		}
	}
}

func TestParseStatusReportsMissingRatherThanZero(t *testing.T) {
	// A kernel thread has no VmHWM. Absence must be distinguishable
	// from a measured zero, which is the whole reason `have` exists.
	_, _, _, have := parseStatus("Name:\tkthreadd\nThreads:\t1\n")
	for _, k := range []string{"hwm", "vol", "invol"} {
		if have[k] {
			t.Errorf("parseStatus claimed %q was present in a status without it", k)
		}
	}
}

func TestLiveReadsThisProcess(t *testing.T) {
	s, err := Live("self-test", os.Getpid())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if s.MaxRSSKiB <= 0 {
		t.Errorf("MaxRSSKiB = %d, want the real peak RSS", s.MaxRSSKiB)
	}
	// Cross-check the peak RSS against /proc read independently: if Live
	// mis-parsed the field it would not agree with the file.
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skip("cannot read /proc/self/status")
	}
	var want int64
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			want, _ = strconv.ParseInt(strings.Fields(line)[1], 10, 64)
		}
	}
	// VmHWM is a high-water mark, so the later read can only be >= the
	// earlier one — comparing two samples for equality would be wrong by
	// construction and did fail once the test itself allocated between
	// them. What a mis-parse would break is the magnitude, so assert
	// monotonicity plus the same ballpark.
	if want > 0 {
		if s.MaxRSSKiB > want {
			t.Errorf("MaxRSSKiB = %d > later /proc read %d; VmHWM cannot shrink",
				s.MaxRSSKiB, want)
		}
		if s.MaxRSSKiB*2 < want {
			t.Errorf("MaxRSSKiB = %d, /proc/self/status says %d — not the same field",
				s.MaxRSSKiB, want)
		}
	}
	// Block I/O is not available per live task; it must be declared, not
	// silently reported as zero.
	if !strings.Contains(strings.Join(s.Partial, ","), "BlockInputOps") {
		t.Errorf("Partial = %v, want BlockInputOps declared unavailable", s.Partial)
	}
}

func TestLiveRejectsNoProcess(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if _, err := Live("x", pid); err == nil {
			t.Errorf("Live(pid=%d) succeeded, want an error", pid)
		}
	}
}

func TestLiveOnAbsentPIDFails(t *testing.T) {
	// A pid that cannot exist: the max is 2^22 on 64-bit Linux.
	if _, err := Live("gone", 1<<23); err == nil {
		t.Error("Live() on a nonexistent pid succeeded")
	}
}
