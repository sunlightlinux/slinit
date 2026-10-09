package service

import (
	"os/exec"
	"testing"
	"time"
)

// A daemon that has exited but is not yet reaped — its parent is some
// other init, as for a user instance or inside a container — still
// answers kill(pid, 0). Stop detection has to treat it as gone, or the
// stop waits for the reaper (seen in TestBGProcessServiceWithDependency
// as stops taking a full extra poll interval, or more).
func TestProcIsZombie(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(3 * time.Second)
	for !procIsZombie(pid) {
		if time.Now().After(deadline) {
			cmd.Wait()
			t.Fatal("exited, unreaped child not reported as a zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cmd.Wait()

	live := exec.Command("/bin/sleep", "5")
	if err := live.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { live.Process.Kill(); live.Wait() }()
	if procIsZombie(live.Process.Pid) {
		t.Error("running process reported as a zombie")
	}
}
