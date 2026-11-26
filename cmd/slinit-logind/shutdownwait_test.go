package main

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"
)

// zombieChildren counts this process's children that have exited and
// not been waited for.
func zombieChildren(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		t.Skip("no /proc")
	}
	me := " " + strconv.Itoa(os.Getpid()) + " "
	n := 0
	for _, e := range ents {
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 || len(s) < i+4 {
			continue
		}
		// After "comm) ": state, then ppid.
		if s[i+2] == 'Z' && strings.HasPrefix(s[i+3:], me) {
			n++
		}
	}
	return n
}

func countPidfds(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	n := 0
	for _, e := range ents {
		if l, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil && strings.Contains(l, "pidfd") {
			n++
		}
	}
	return n
}

// runShutdown started slinit-shutdown and never waited for it. When the
// request does not take the machine down (slinit-shutdown refused, or
// failed), each power-key press or PowerOff/Reboot call left a zombie
// child of logind for good, and a pidfd that only a garbage collection
// closed.
func TestRunShutdownReapsItsChild(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	saved := slinitctlPath
	slinitctlPath = "/bin/false"
	t.Cleanup(func() { slinitctlPath = saved })

	zombies, pidfds := zombieChildren(t), countPidfds(t)
	const rounds = 50
	for i := 0; i < rounds; i++ {
		if err := runShutdown("reboot"); err != nil {
			t.Fatal(err)
		}
	}
	var z, p int
	for deadline := time.Now().Add(5 * time.Second); ; {
		z, p = zombieChildren(t)-zombies, countPidfds(t)-pidfds
		if (z == 0 && p == 0) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if z != 0 || p != 0 {
		t.Errorf("after %d failed shutdown requests: %d zombie children, %d pidfds open; want 0 and 0", rounds, z, p)
	}
}
