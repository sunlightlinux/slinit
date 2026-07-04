package main

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestParseNotifyAcceptsAllModes(t *testing.T) {
	cases := []struct {
		spec string
		mode string
		fd   int
		sig  syscall.Signal
	}{
		{"", "none", 0, 0},
		{"readiness=none", "none", 0, 0},
		{"readiness=manual", "manual", 0, 0},
		{"readiness=pidfile", "pidfile", 0, 0},
		{"readiness=stderr", "stderr", 0, 0},
		{"readiness=fd:3", "fd", 3, 0},
		{"readiness=fd:9", "fd", 9, 0},
		{"readiness=signal", "signal", 0, syscall.SIGUSR1},
		{"readiness=signal:SIGUSR2", "signal", 0, syscall.SIGUSR2},
		{"readiness=signal:TERM", "signal", 0, syscall.SIGTERM},
	}
	for _, tc := range cases {
		got, err := parseNotify(tc.spec)
		if err != nil {
			t.Errorf("parseNotify(%q): %v", tc.spec, err)
			continue
		}
		if got.mode != tc.mode {
			t.Errorf("parseNotify(%q).mode = %q, want %q", tc.spec, got.mode, tc.mode)
		}
		if got.fdNum != tc.fd {
			t.Errorf("parseNotify(%q).fdNum = %d, want %d", tc.spec, got.fdNum, tc.fd)
		}
		if got.signal != tc.sig {
			t.Errorf("parseNotify(%q).signal = %d, want %d", tc.spec, got.signal, tc.sig)
		}
	}
}

func TestParseNotifyRejectsMalformed(t *testing.T) {
	bad := []string{
		"bogus",
		"readiness=",
		"readiness=lolwat",
		"readiness=fd:",
		"readiness=fd:abc",
		"readiness=fd:2",  // stdio
		"readiness=fd:10", // out of pad range
		"readiness=signal:UNKNOWN",
	}
	for _, s := range bad {
		if _, err := parseNotify(s); err == nil {
			t.Errorf("parseNotify(%q): expected error", s)
		}
	}
}

// TestNotifyFDReadinessSignals runs a shell child that writes "READY\n"
// to fd 3 after a short delay, and verifies notifyState.wait returns
// exitOK.
func TestNotifyFDReadinessSignals(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "notify.sh")
	// Sleep 200ms then write READY to fd 3, then hold the process open
	// so the wait side is exercising the ready path rather than a race
	// with process exit.
	body := "#!/bin/sh\n(sleep 0.2; echo READY >&3) & sleep 5\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(sh, script)
	// Wire fd 3 in the child (ExtraFiles[0] == fd 3).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	w.Close() // parent releases its ref, matching postFork()

	st := &notifyState{
		proto:   notifyProto{mode: "fd", fdNum: 3},
		readEnd: r,
	}
	start := time.Now()
	code := st.wait(Options{Wait: 3000}, cmd.Process.Pid)
	elapsed := time.Since(start)
	if code != exitOK {
		t.Errorf("fd wait: got %d, want %d", code, exitOK)
	}
	if elapsed < 100*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("elapsed=%v, expected roughly 200ms-2s", elapsed)
	}
}

func TestNotifyFDTimesOutWithoutData(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep: %v", err)
	}
	cmd := exec.Command(sleep, "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Immediately close write end so nothing is ever written; the read
	// side sees EOF and wait must classify that as failure.
	w.Close()

	st := &notifyState{
		proto:   notifyProto{mode: "fd", fdNum: 3},
		readEnd: r,
	}
	if code := st.wait(Options{Wait: 500}, cmd.Process.Pid); code == exitOK {
		t.Errorf("empty EOF should not be treated as ready")
	}
}

// TestNotifyStderrFirstLine spawns `sh -c 'echo READY; sleep 5'` with
// stderr captured; the wait must return once the first non-empty line
// arrives.
func TestNotifyStderrFirstLine(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	cmd := exec.Command(sh, "-c", "sleep 0.15; echo READY >&2; sleep 5")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	w.Close() // parent-side cleanup

	st := &notifyState{
		proto:   notifyProto{mode: "stderr"},
		readEnd: r,
	}
	code := st.wait(Options{Wait: 3000}, cmd.Process.Pid)
	if code != exitOK {
		t.Errorf("stderr wait: got %d, want %d", code, exitOK)
	}
}

// TestNotifySignalReceived asks a subshell to signal the parent
// (getppid) after a short delay and verifies wait returns exitOK.
func TestNotifySignalReceived(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	// signal.Notify must be armed before the child sends. Do it here
	// to mirror what prepareNotify does pre-fork.
	sigCh := make(chan os.Signal, 1)
	// Use SIGUSR2 to keep this test isolated from any global SIGUSR1
	// handlers a test harness might install.
	sig := syscall.SIGUSR2
	signal.Notify(sigCh, sig)
	defer signal.Stop(sigCh)

	ppid := os.Getpid()
	body := "kill -USR2 " + strconv.Itoa(ppid) + "; sleep 5"
	cmd := exec.Command(sh, "-c", "sleep 0.15; "+body)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	st := &notifyState{
		proto: notifyProto{mode: "signal", signal: sig},
		sigCh: sigCh,
	}
	code := st.wait(Options{Wait: 3000}, cmd.Process.Pid)
	if code != exitOK {
		t.Errorf("signal wait: got %d, want %d", code, exitOK)
	}
}

func TestNotifySignalTimesOut(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep: %v", err)
	}
	sigCh := make(chan os.Signal, 1)
	sig := syscall.SIGUSR2
	signal.Notify(sigCh, sig)
	defer signal.Stop(sigCh)

	cmd := exec.Command(sleep, "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	st := &notifyState{
		proto: notifyProto{mode: "signal", signal: sig},
		sigCh: sigCh,
	}
	if code := st.wait(Options{Wait: 400}, cmd.Process.Pid); code == exitOK {
		t.Errorf("expected timeout, got %d", code)
	}
}

// TestPrepareNotifyFDBuildsExtraFiles ensures that fd:N pads
// ExtraFiles with placeholder entries so the pipe lands at exactly the
// requested slot in the child's fd table.
func TestPrepareNotifyFDBuildsExtraFiles(t *testing.T) {
	cmd := exec.Command("/bin/true")
	opts := Options{Notify: "readiness=fd:5"}
	st, err := prepareNotify(cmd, opts)
	if err != nil {
		t.Fatalf("prepareNotify: %v", err)
	}
	defer st.closeAll()
	// fd:5 = 2 pad slots (fd 3, 4) + 1 pipe slot (fd 5) = 3 entries.
	if got, want := len(cmd.ExtraFiles), 3; got != want {
		t.Errorf("ExtraFiles len = %d, want %d", got, want)
	}
	if st.readEnd == nil || st.writeEnd == nil {
		t.Errorf("pipe not wired: readEnd=%v writeEnd=%v", st.readEnd, st.writeEnd)
	}
	if got, want := len(st.pads), 2; got != want {
		t.Errorf("pads len = %d, want %d", got, want)
	}
}

func TestPrepareNotifyStderrRejectsConflict(t *testing.T) {
	// StderrLogger set: prepareNotify(readiness=stderr) must refuse.
	cmd := exec.Command("/bin/true")
	opts := Options{Notify: "readiness=stderr", StderrLogger: "logger"}
	if _, err := prepareNotify(cmd, opts); err == nil {
		t.Errorf("expected conflict error for --stderr-logger + readiness=stderr")
	}
	// --stderr file set: same.
	opts = Options{Notify: "readiness=stderr", Stderr: "/tmp/x"}
	if _, err := prepareNotify(exec.Command("/bin/true"), opts); err == nil {
		t.Errorf("expected conflict error for --stderr + readiness=stderr")
	}
}

