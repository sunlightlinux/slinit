package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildBinary compiles slinit-supervise-daemon for tests that need the
// real re-exec: isSupervisor compares executables, so --stop and
// --signal only recognise a supervisor started by the same binary.
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "slinit-supervise-daemon")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("building binary: %v\n%s", err, out)
	}
	return bin
}

// runBin runs the binary with env added to a clean copy of ours and
// returns its exit code and combined output.
func runBin(t *testing.T, bin string, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(scrubEnviron(os.Environ()), env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err == nil {
		return 0, out.String()
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String()
	}
	t.Fatalf("run %v: %v", args, err)
	return -1, ""
}

// pidOf reads a pidfile, failing the test if it is not there.
func pidOf(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return pid
}

// waitFile waits for path to exist.
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

// startSupervised starts a supervisor through the binary and registers
// a cleanup that kills whatever the pidfiles named at start.
func startSupervised(t *testing.T, bin, pidfile string, args ...string) (super, daemon int) {
	t.Helper()
	all := append([]string{"svc", "--start", "--pidfile", pidfile}, args...)
	if code, out := runBin(t, bin, nil, all...); code != 0 {
		t.Fatalf("--start: exit %d: %s", code, out)
	}
	waitFile(t, pidfile+".daemon")
	super, daemon = pidOf(t, pidfile), pidOf(t, pidfile+".daemon")
	t.Cleanup(func() {
		_ = syscall.Kill(super, syscall.SIGKILL)
		_ = syscall.Kill(daemon, syscall.SIGKILL)
	})
	return super, daemon
}

// The supervisor switch was an environment variable the daemon, and
// everything it ran, inherited. A --stop issued from inside that
// environment then became a supervisor itself: it rewrote and removed
// the pidfiles and left the real supervisor and daemon untracked.
func TestSupervisorSwitchNotInherited(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "svc.pid")
	envOut := filepath.Join(dir, "env.out")
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	super, daemon := startSupervised(t, bin, pidfile,
		"--exec", sh, "--", "-c", `env > "$0"; exec sleep 60`, envOut)

	waitFile(t, envOut)
	time.Sleep(50 * time.Millisecond)
	env, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, []byte(legacySupervisorEnvVar)) {
		t.Errorf("daemon inherited %s", legacySupervisorEnvVar)
	}

	// A --stop run where the variable is set (a daemon started by an
	// older supervisor, or a shell under one) must still be a --stop.
	code, out := runBin(t, bin, []string{legacySupervisorEnvVar + "=1"},
		"svc", "--stop", "--pidfile", pidfile)
	if code != exitOK {
		t.Errorf("--stop with %s set: exit %d: %s", legacySupervisorEnvVar, code, out)
	}
	if !waitExit(super, 5*time.Second) {
		t.Errorf("supervisor pid %d still running after --stop", super)
	}
	if !waitExit(daemon, 5*time.Second) {
		t.Errorf("daemon pid %d still running after --stop", daemon)
	}
}

// The marker means "become the supervisor" only for --start.
func TestSplitSupervisorMarker(t *testing.T) {
	in, rest := splitSupervisorMarker([]string{supervisorMarker, "svc", "--stop"})
	if !in || len(rest) != 2 || rest[0] != "svc" {
		t.Errorf("marker first: got %v %v", in, rest)
	}
	if in, _ := splitSupervisorMarker([]string{"svc", supervisorMarker}); in {
		t.Errorf("marker not first was taken as the switch")
	}
	bin := buildBinary(t)
	pidfile := filepath.Join(t.TempDir(), "svc.pid")
	if code, _ := runBin(t, bin, nil, supervisorMarker, "svc", "--stop", "--pidfile", pidfile); code != exitBadUsage {
		t.Errorf("marker with --stop: exit %d, want %d", code, exitBadUsage)
	}
	if _, err := os.Stat(pidfile); err == nil {
		t.Errorf("marker with --stop wrote the pidfile")
	}
}

// startDaemon must not pass the legacy switch on even when the
// supervisor's own environment carries it.
func TestStartDaemonScrubsSupervisorSwitch(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	t.Setenv(legacySupervisorEnvVar, "1")
	out := filepath.Join(t.TempDir(), "env.out")
	opts := Options{Service: "test", Exec: sh, Args: []string{"-c", `env > "$0"`, out}}
	_, exitCh, err := startDaemon(opts)
	if err != nil {
		t.Fatal(err)
	}
	<-exitCh
	env, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, []byte(legacySupervisorEnvVar)) {
		t.Errorf("daemon inherited %s", legacySupervisorEnvVar)
	}
}
