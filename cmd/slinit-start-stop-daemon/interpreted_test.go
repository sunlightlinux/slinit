package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestIsInterpreter(t *testing.T) {
	yes := []string{"/bin/sh", "/bin/bash", "/usr/bin/python3", "/usr/bin/perl"}
	no := []string{"/usr/sbin/nginx", "/usr/bin/foo", "/bin/true"}
	for _, p := range yes {
		if !isInterpreter(p) {
			t.Errorf("isInterpreter(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if isInterpreter(p) {
			t.Errorf("isInterpreter(%q) = true, want false", p)
		}
	}
}

// TestInterpretedMatchesScriptPath spawns `sh SCRIPT` and verifies
// --interpreted + --name=SCRIPT-basename matches the process, whereas
// without --interpreted the comm would just be "sh".
func TestInterpretedMatchesScriptPath(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "myservice.sh")
	// Long-running loop the parent will kill in Cleanup.
	body := "#!/bin/sh\nwhile true; do sleep 1; done\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(shPath, script)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	// Give the kernel a beat to populate /proc/PID/{exe,cmdline}.
	time.Sleep(150 * time.Millisecond)

	// Without --interpreted: comm is "sh", so matching on "myservice.sh" fails.
	m := MatchCriteria{Name: "myservice.sh", UID: -1}
	if processMatches(pid, m) {
		t.Error("plain --name should NOT match interpreter comm")
	}

	// With --interpreted: name comparison switches to argv[1] basename.
	m.Interpreted = true
	if !processMatches(pid, m) {
		t.Error("--interpreted --name=myservice.sh should match")
	}

	// Exec path: without --interpreted, exe is sh, not the script.
	m = MatchCriteria{Exec: script, UID: -1}
	if processMatches(pid, m) {
		t.Error("plain --exec should NOT match script when process is sh")
	}
	m.Interpreted = true
	if !processMatches(pid, m) {
		t.Error("--interpreted --exec=SCRIPT should match interpreter+script")
	}
}
