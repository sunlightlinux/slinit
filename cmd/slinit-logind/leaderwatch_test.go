package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"
)

// osThreads is the number of OS threads in this process.
func osThreads(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("no /proc/self/status: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "Threads:" {
			n, _ := strconv.Atoi(f[1])
			return n
		}
	}
	t.Fatal("no Threads: line in /proc/self/status")
	return 0
}

// openFDs is the number of descriptors open in this process.
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	return len(ents)
}

// liveLeader starts a process that stands in for a session leader.
func liveLeader(t *testing.T) uint32 {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return uint32(cmd.Process.Pid)
}

// settle polls cond for up to 5s.
func settle(cond func() bool) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// The leader watch used to park one goroutine in poll(2) per session, and
// a goroutine in a system call holds an OS thread: a server with hundreds
// of SSH logins held hundreds of threads in logind, each a task counted
// against RLIMIT_NPROC and pids.max. At the limit the Go runtime's next
// pthread_create fails and logind aborts, taking every login with it.
func TestLeaderWatchesDoNotHoldThreads(t *testing.T) {
	m := usersBus(t)
	leader := liveLeader(t)
	before := osThreads(t)
	const n = 300
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", 100+i)
		withSession(t, id, 1000)
		m.watchSessionLeader(id, leader)
	}
	// Give every watcher time to reach its wait.
	time.Sleep(500 * time.Millisecond)
	after := osThreads(t)
	t.Logf("threads: %d before, %d with %d leader watches", before, after, n)
	if after-before > 32 {
		t.Errorf("%d leader watches added %d OS threads; the watch must wait in the netpoller", n, after-before)
	}
	for i := 0; i < n; i++ {
		_ = m.releaseSession(fmt.Sprintf("c%d", 100+i))
	}
}

// ReleaseSession (pam_close_session) must end the session's leader watch.
// It did not: the goroutine and its pidfd stayed until the leader exited,
// and a leader that outlives its session (a daemonised process, a nohup'd
// job) kept them for good, one more with every login.
func TestReleaseSessionCancelsLeaderWatch(t *testing.T) {
	// With the GC off no finalizer can close a leaked *os.File for us.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	m := usersBus(t)
	leader := liveLeader(t)
	time.Sleep(50 * time.Millisecond)
	baseG, baseFD := runtime.NumGoroutine(), openFDs(t)
	const n = 50
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", 500+i)
		withSession(t, id, 1000)
		m.watchSessionLeader(id, leader)
	}
	if !settle(func() bool { return openFDs(t) >= baseFD+n }) {
		t.Fatalf("watches armed: %d fds, want at least %d", openFDs(t), baseFD+n)
	}
	for i := 0; i < n; i++ {
		if err := m.releaseSession(fmt.Sprintf("c%d", 500+i)); err != nil {
			t.Fatalf("releaseSession: %v", err)
		}
	}
	ok := settle(func() bool { return runtime.NumGoroutine() <= baseG && openFDs(t) <= baseFD })
	if !ok {
		t.Errorf("after releasing %d sessions whose leader lives on: %d goroutines (baseline %d), %d fds (baseline %d)",
			n, runtime.NumGoroutine(), baseG, openFDs(t), baseFD)
	}
}

// A watch still releases the session when its leader exits.
func TestLeaderExitStillReleasesSession(t *testing.T) {
	m := usersBus(t)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn helper: %v", err)
	}
	withSession(t, "c42", 1000)
	m.watchSessionLeader("c42", uint32(cmd.Process.Pid))
	time.Sleep(100 * time.Millisecond)
	if !fileExists(sessionFile("c42")) {
		t.Fatal("session released while its leader lives")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if !settle(func() bool { return !fileExists(sessionFile("c42")) }) {
		t.Error("session not released after its leader exited")
	}
	// Let releaseSession finish before the test's state root goes away.
	sessionMu.Lock()
	sessionMu.Unlock() //lint:ignore SA2001 a barrier, not a critical section
}
