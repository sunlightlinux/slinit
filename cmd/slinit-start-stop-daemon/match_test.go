package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestReadPIDFileBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.pid")
	if err := os.WriteFile(path, []byte("1234\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 1234 {
		t.Errorf("pid=%d, want 1234", pid)
	}
}

func TestReadPIDFileMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.pid")
	if err := os.WriteFile(path, []byte("garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	// Malformed file → treat as "not running" (pid=0, no error).
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if pid != 0 {
		t.Errorf("pid=%d, want 0", pid)
	}
}

func TestReadPIDFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.pid")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if pid != 0 {
		t.Errorf("pid=%d, want 0", pid)
	}
}

func TestWriteAndReadPIDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "svc.pid")
	if err := writePIDFile(path, 42); err != nil {
		t.Fatal(err)
	}
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 42 {
		t.Errorf("pid=%d, want 42", pid)
	}
}

// TestFindMatchingPIDsSelfExec proves the match logic against a real
// running process: our own /proc/self/exe.
func TestFindMatchingPIDsSelfExec(t *testing.T) {
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		t.Skipf("cannot read /proc/self/exe: %v", err)
	}
	pids, err := FindMatchingPIDs(MatchCriteria{Exec: self, UID: -1})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	me := os.Getpid()
	for _, p := range pids {
		if p == me {
			// The scan excludes self by design; a match should still be
			// impossible for our own pid.
			found = true
		}
	}
	if found {
		t.Error("scan returned our own pid, but self-exclusion should have hidden it")
	}
}

func TestFindMatchingPIDsPidFileStale(t *testing.T) {
	// Pidfile pointing at a pid we can be certain is missing.
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(999_999)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pids, err := FindMatchingPIDs(MatchCriteria{PidFile: path, UID: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 0 {
		t.Errorf("stale pidfile should yield no matches, got %v", pids)
	}
}

func TestFindMatchingPIDsMissingPidFile(t *testing.T) {
	pids, err := FindMatchingPIDs(MatchCriteria{PidFile: "/nonexistent/path.pid", UID: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 0 {
		t.Errorf("missing pidfile should yield no matches, got %v", pids)
	}
}

// TestFindMatchingPIDsNeverMatchesInit: with only --pidfile, whatever
// pid the file held was signalled — PID 1 included, where SIGTERM is
// a reboot under slinit. Neither init nor the caller's own process
// tree (our pid, our parent) may ever be a match.
func TestFindMatchingPIDsNeverMatchesInit(t *testing.T) {
	for _, pid := range []int{1, os.Getpid(), os.Getppid()} {
		path := filepath.Join(t.TempDir(), "svc.pid")
		if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, 0o644) // umask-proof
		got, err := FindMatchingPIDs(MatchCriteria{PidFile: path, UID: -1})
		if err != nil {
			t.Fatalf("pid %d: %v", pid, err)
		}
		if len(got) != 0 {
			t.Errorf("pidfile holding %d matched %v", pid, got)
		}
	}
}

// TestFindMatchingPIDsRefusesWorldWritablePidfile: anyone could
// write a world-writable pidfile and choose what we signal. Debian's
// start-stop-daemon refuses it ("matching on world-writable pidfile
// is insecure"); so do we.
func TestFindMatchingPIDsRefusesWorldWritablePidfile(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	path := filepath.Join(t.TempDir(), "svc.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := FindMatchingPIDs(MatchCriteria{PidFile: path, UID: -1}); err != nil || len(got) != 1 {
		t.Fatalf("0644 pidfile: got %v, %v; want the sleep pid", got, err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if got, err := FindMatchingPIDs(MatchCriteria{PidFile: path, UID: -1}); err == nil {
		t.Errorf("world-writable pidfile accepted: matched %v", got)
	}
}

// TestExecMatchesDeletedBinary: after a package upgrade replaces the
// binary, /proc/PID/exe of the running daemon reads "<path> (deleted)".
// An exact compare found nothing: --stop left it running and --start
// launched a second instance. OpenRC and Debian strip the suffix.
func TestExecMatchesDeletedBinary(t *testing.T) {
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skip(err)
	}
	bin := filepath.Join(t.TempDir(), "mydaemon")
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	pid := cmd.Process.Pid
	if !processMatches(pid, MatchCriteria{Exec: bin, UID: -1}) {
		t.Fatal("did not match the live binary")
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if !processMatches(pid, MatchCriteria{Exec: bin, UID: -1}) {
		exe, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
		t.Errorf("--exec %s did not match after the binary was replaced (exe=%q)", bin, exe)
	}
	if processMatches(pid, MatchCriteria{Exec: bin + "2", UID: -1}) {
		t.Error("matched a different path")
	}
}

// startCopiedSleep copies sleep(1) into dir/bin/<name>, makes dir/sbin
// a symlink to bin (the merged-/usr layout) and starts it through the
// sbin spelling. Returns the pid and the sbin path.
func startCopiedSleep(t *testing.T, name string) (int, string) {
	t.Helper()
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bin", filepath.Join(dir, "sbin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", name), data, 0755); err != nil {
		t.Fatal(err)
	}
	viaSbin := filepath.Join(dir, "sbin", name)
	cmd := exec.Command(viaSbin, "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("copied sleep does not run: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	// Wait for the exec to land so /proc/PID/exe is the copy.
	for i := 0; i < 50; i++ {
		if l, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe"); filepath.Base(l) == name {
			return pid, viaSbin
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Skip("copied sleep never exec'd (multi-call binary?)")
	return 0, ""
}

// TestExecMatchThroughSymlinkedDir is the merged-/usr case: the daemon
// was started as dir/sbin/foo, the kernel reports dir/bin/foo, and
// --exec dir/sbin/foo must still find it (Debian compares dev+inode).
func TestExecMatchThroughSymlinkedDir(t *testing.T) {
	pid, viaSbin := startCopiedSleep(t, "mydaemon")
	pids, err := FindMatchingPIDs(MatchCriteria{Exec: viaSbin, UID: -1})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pids {
		if p == pid {
			found = true
		}
	}
	if !found {
		t.Fatalf("--exec %s did not match pid %d (exe=%s); got %v",
			viaSbin, pid, mustReadlink("/proc/"+strconv.Itoa(pid)+"/exe"), pids)
	}

	// A different file with the same content is NOT the same executable.
	other := filepath.Join(t.TempDir(), "mydaemon")
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(viaSbin)), "bin", "mydaemon"))
	if err := os.WriteFile(other, data, 0755); err != nil {
		t.Fatal(err)
	}
	if processMatches(pid, MatchCriteria{Exec: other, UID: -1}) {
		t.Error("a copy at another inode must not match")
	}
}

// TestExecMatchReplacedBinary: an upgrade replaced the binary, so the
// running image is a deleted inode no stat of --exec reaches; the
// string fallback (" (deleted)" stripped) must still match it.
func TestExecMatchReplacedBinary(t *testing.T) {
	pid, viaSbin := startCopiedSleep(t, "mydaemon")
	binPath := filepath.Join(filepath.Dir(filepath.Dir(viaSbin)), "bin", "mydaemon")
	data, _ := os.ReadFile(binPath)
	if err := os.Remove(binPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, data, 0755); err != nil {
		t.Fatal(err)
	}
	if !processMatches(pid, MatchCriteria{Exec: binPath, UID: -1}) {
		t.Errorf("replaced binary: --exec %s should still match (exe=%s)",
			binPath, mustReadlink("/proc/"+strconv.Itoa(pid)+"/exe"))
	}
}

func mustReadlink(p string) string {
	s, _ := os.Readlink(p)
	return s
}
