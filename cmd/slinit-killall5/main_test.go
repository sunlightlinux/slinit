package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// statLine builds a /proc/<pid>/stat line with the fields this tool
// reads at their real positions: state 3, session 6, startcode 26,
// endcode 27. Everything else is filler, because nothing reads it.
func statLine(pid int, comm, state string, session int, startcode, endcode uint64) string {
	f := make([]string, 25)
	for i := range f {
		f[i] = "0"
	}
	f[0] = state
	f[3] = fmt.Sprint(session)
	f[23] = fmt.Sprint(startcode)
	f[24] = fmt.Sprint(endcode)
	return fmt.Sprintf("%d (%s) %s\n", pid, comm, strings.Join(f, " "))
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantSig syscall.Signal
		wantOmt []int
		wantErr bool
	}{
		{"term", []string{"-15"}, syscall.SIGTERM, nil, false},
		{"kill", []string{"-9"}, syscall.SIGKILL, nil, false},
		{"omit one", []string{"-15", "-o", "42"}, syscall.SIGTERM, []int{42}, false},
		{"omit list", []string{"-15", "-o", "42,43"}, syscall.SIGTERM, []int{42, 43}, false},
		// Upstream's parser accepts both spellings, so a drop-in has to.
		{"omit attached", []string{"-15", "-o42"}, syscall.SIGTERM, []int{42}, false},
		{"omit repeated", []string{"-9", "-o", "1", "-o", "2,3"}, syscall.SIGKILL, []int{1, 2, 3}, false},
		{"signal last", []string{"-o", "7", "-15"}, syscall.SIGTERM, []int{7}, false},

		{"no signal", []string{"-o", "5"}, 0, nil, true},
		{"two signals", []string{"-15", "-9"}, 0, nil, true},
		{"signal zero", []string{"-0"}, 0, nil, true},
		{"signal too big", []string{"-99"}, 0, nil, true},
		{"omit without value", []string{"-15", "-o"}, 0, nil, true},
		{"omit not a number", []string{"-15", "-o", "abc"}, 0, nil, true},
		{"omit negative", []string{"-15", "-o", "-3"}, 0, nil, true},
		{"stray argument", []string{"-15", "nginx"}, 0, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig, omit, err := parseArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got sig=%v omit=%v", sig, omit)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sig != tc.wantSig {
				t.Errorf("signal = %v, want %v", sig, tc.wantSig)
			}
			var got []int
			for p := range omit {
				got = append(got, p)
			}
			sort.Ints(got)
			if fmt.Sprint(got) != fmt.Sprint(tc.wantOmt) {
				t.Errorf("omit = %v, want %v", got, tc.wantOmt)
			}
		})
	}
}

func TestParseStat(t *testing.T) {
	t.Run("ordinary process", func(t *testing.T) {
		p, ok := parseStat(100, statLine(100, "nginx", "S", 100, 0x400000, 0x4a0000))
		if !ok {
			t.Fatal("failed to parse")
		}
		if p.sid != 100 || p.kernel || p.zombie {
			t.Errorf("got %+v, want sid=100 and neither kernel nor zombie", p)
		}
	})

	// A kernel thread has no user address space, which is how upstream
	// recognises one: startcode and endcode are both zero.
	t.Run("kernel thread", func(t *testing.T) {
		p, ok := parseStat(7, statLine(7, "kworker/0:1", "S", 0, 0, 0))
		if !ok || !p.kernel {
			t.Errorf("got %+v, ok=%v — want kernel=true", p, ok)
		}
	})

	t.Run("zombie", func(t *testing.T) {
		p, ok := parseStat(200, statLine(200, "gone", "Z", 200, 0x400000, 0x4a0000))
		if !ok || !p.zombie {
			t.Errorf("got %+v, ok=%v — want zombie=true", p, ok)
		}
	})

	// The one that breaks any left-to-right field count: a process may
	// legally be named ") (" and field 2 is the name, in parentheses.
	// Indexing from the LAST ')' is the only thing that survives it.
	t.Run("comm containing parentheses and spaces", func(t *testing.T) {
		p, ok := parseStat(300, statLine(300, ") (evil name) (", "S", 303, 0x400000, 0x4a0000))
		if !ok {
			t.Fatal("failed to parse a legal comm")
		}
		if p.sid != 303 {
			t.Errorf("sid = %d, want 303 — fields were counted from the wrong place", p.sid)
		}
		if p.kernel || p.zombie {
			t.Errorf("got %+v, want neither kernel nor zombie", p)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		if _, ok := parseStat(1, "1 (init) S 0 0 1\n"); ok {
			t.Error("a short stat line was accepted")
		}
		if _, ok := parseStat(1, "garbage"); ok {
			t.Error("a line with no ')' was accepted")
		}
	})
}

// The selection rule, stated. Getting any of these wrong on a shutdown
// path either kills the script doing the shutdown or claims a kill that
// never happened.
func TestSkip(t *testing.T) {
	const self, sid = 500, 400
	cases := []struct {
		name string
		p    procInfo
		omit map[int]bool
		want bool
	}{
		{"init", procInfo{pid: 1, sid: 1}, nil, true},
		{"ourselves", procInfo{pid: self, sid: 999}, nil, true},
		{"our own session", procInfo{pid: 501, sid: sid}, nil, true},
		{"kernel thread", procInfo{pid: 7, sid: 0, kernel: true}, nil, true},
		{"zombie", procInfo{pid: 600, sid: 600, zombie: true}, nil, true},
		{"omitted", procInfo{pid: 700, sid: 700}, map[int]bool{700: true}, true},
		{"an ordinary process", procInfo{pid: 800, sid: 800}, nil, false},
		{"omitted list without a match", procInfo{pid: 800, sid: 800}, map[int]bool{700: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skip(tc.p, self, sid, tc.omit); got != tc.want {
				t.Errorf("skip = %v, want %v", got, tc.want)
			}
		})
	}
}

// fakeProc builds a /proc with the given stat lines and points the tool
// at it, so the end-to-end path can run without signalling anything real.
func fakeProc(t *testing.T, lines map[int]string) {
	t.Helper()
	dir := t.TempDir()
	for pid, line := range lines {
		d := filepath.Join(dir, fmt.Sprint(pid))
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "stat"), []byte(line), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Entries that are not pids must be ignored, the way /proc's own
	// cpuinfo and self are.
	for _, junk := range []string{"self", "cpuinfo", "net"} {
		if err := os.MkdirAll(filepath.Join(dir, junk), 0755); err != nil {
			t.Fatal(err)
		}
	}
	old := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = old })
}

func withFakeIdentity(t *testing.T, pid, sid int) {
	t.Helper()
	op, os_ := getpidFunc, getsidFunc
	getpidFunc = func() int { return pid }
	getsidFunc = func() int { return sid }
	t.Cleanup(func() { getpidFunc, getsidFunc = op, os_ })
}

type signalled struct {
	pid int
	sig syscall.Signal
}

func captureKills(t *testing.T) *[]signalled {
	t.Helper()
	var seen []signalled
	old := killFunc
	killFunc = func(pid int, sig syscall.Signal) error {
		seen = append(seen, signalled{pid, sig})
		return nil
	}
	t.Cleanup(func() { killFunc = old })
	return &seen
}

func TestRunSignalsOnlyWhatItShould(t *testing.T) {
	fakeProc(t, map[int]string{
		1:   statLine(1, "slinit", "S", 1, 0x400000, 0x4a0000),
		7:   statLine(7, "kworker/0:1", "S", 0, 0, 0),                       // kernel
		500: statLine(500, "slinit-killall5", "R", 400, 0x400000, 0x4a0000), // self
		501: statLine(501, "sh", "S", 400, 0x400000, 0x4a0000),              // our session
		600: statLine(600, "nginx", "S", 600, 0x400000, 0x4a0000),
		601: statLine(601, "postgres", "S", 601, 0x400000, 0x4a0000),
		602: statLine(602, "gone", "Z", 602, 0x400000, 0x4a0000), // zombie
	})
	withFakeIdentity(t, 500, 400)
	seen := captureKills(t)

	if rc := run([]string{"-15"}, os.Stderr); rc != exitKilled {
		t.Errorf("exit = %d, want %d", rc, exitKilled)
	}

	var pids []int
	for _, s := range *seen {
		// The freeze and resume are sent to -1; the kills are per pid.
		if s.pid == -1 {
			continue
		}
		pids = append(pids, s.pid)
		if s.sig != syscall.SIGTERM {
			t.Errorf("pid %d got %v, want SIGTERM", s.pid, s.sig)
		}
	}
	sort.Ints(pids)
	if fmt.Sprint(pids) != "[600 601]" {
		t.Errorf("signalled %v, want [600 601] — init, self, our session, the "+
			"kernel thread and the zombie must all be spared", pids)
	}
}

// Upstream freezes the world with kill(-1, SIGSTOP) before reading /proc
// so the process list cannot change underneath it, and resumes with
// SIGCONT. It does this ONLY when there is no omit list — with -o the
// caller is usually a script that has to keep running.
func TestFreezeOnlyWithoutAnOmitList(t *testing.T) {
	procs := map[int]string{
		1:   statLine(1, "slinit", "S", 1, 0x400000, 0x4a0000),
		600: statLine(600, "nginx", "S", 600, 0x400000, 0x4a0000),
	}

	t.Run("no omit list freezes and resumes", func(t *testing.T) {
		fakeProc(t, procs)
		withFakeIdentity(t, 500, 400)
		seen := captureKills(t)
		run([]string{"-15"}, os.Stderr)

		if len(*seen) < 2 {
			t.Fatalf("got %d signals, want a freeze, a kill and a resume", len(*seen))
		}
		first, last := (*seen)[0], (*seen)[len(*seen)-1]
		if first.pid != -1 || first.sig != syscall.SIGSTOP {
			t.Errorf("first signal = %+v, want SIGSTOP to -1", first)
		}
		if last.pid != -1 || last.sig != syscall.SIGCONT {
			t.Errorf("last signal = %+v, want SIGCONT to -1", last)
		}
	})

	t.Run("omit list does not freeze", func(t *testing.T) {
		fakeProc(t, procs)
		withFakeIdentity(t, 500, 400)
		seen := captureKills(t)
		run([]string{"-15", "-o", "999"}, os.Stderr)

		for _, s := range *seen {
			if s.pid == -1 {
				t.Errorf("sent %v to -1 with an omit list present; upstream does not freeze then", s.sig)
			}
		}
	})
}

// Exit 2 means "nothing was killed", which a script may branch on. A run
// that spared everything must not claim success.
func TestExitTwoWhenNothingWasKilled(t *testing.T) {
	fakeProc(t, map[int]string{
		1: statLine(1, "slinit", "S", 1, 0x400000, 0x4a0000),
		7: statLine(7, "kworker/0:1", "S", 0, 0, 0),
	})
	withFakeIdentity(t, 500, 400)
	captureKills(t)

	if rc := run([]string{"-15"}, os.Stderr); rc != exitNotKille {
		t.Errorf("exit = %d, want %d when only init and a kernel thread exist", rc, exitNotKille)
	}
}

// Exit 1 is reserved for "/proc is missing", which is a different
// condition from "nothing to kill" and scripts can tell them apart.
func TestExitOneWithoutProc(t *testing.T) {
	old := procRoot
	procRoot = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { procRoot = old })
	captureKills(t)

	if rc := run([]string{"-15"}, os.Stderr); rc != exitNoProc {
		t.Errorf("exit = %d, want %d", rc, exitNoProc)
	}
}

// A pid that vanishes between readdir and open is dropped, not guessed
// at: the number may already have been recycled onto an unrelated
// process by the time we would signal it.
func TestUnreadableStatIsSkipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "600"), 0755); err != nil {
		t.Fatal(err)
	} // no stat file at all
	if err := os.MkdirAll(filepath.Join(dir, "601"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "601", "stat"),
		[]byte(statLine(601, "nginx", "S", 601, 0x400000, 0x4a0000)), 0644); err != nil {
		t.Fatal(err)
	}
	old := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = old })
	withFakeIdentity(t, 500, 400)
	seen := captureKills(t)

	run([]string{"-15"}, os.Stderr)
	for _, s := range *seen {
		if s.pid == 600 {
			t.Error("signalled a pid whose stat could not be read")
		}
	}
}
