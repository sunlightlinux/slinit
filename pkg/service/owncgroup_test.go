package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// A service without a cgroup of its own shares the daemon's default
// cgroup with every other such service. Its cgroup kill (kill-mode =
// control-group, kill-all-on-stop, oom-policy = kill) used to signal
// every process listed there, and delegate = yes handed that shared
// cgroup to the service's user.
func TestSharedDefaultCgroupIsNotTheServicesOwn(t *testing.T) {
	set, _ := newTestSet()
	shared := t.TempDir()
	set.SetDefaultCgroupPath(shared)

	neighbour := exec.Command("sleep", "30")
	if err := neighbour.Start(); err != nil {
		t.Fatal(err)
	}
	defer neighbour.Process.Kill() // Wait is collected by the goroutine below
	if err := os.WriteFile(filepath.Join(shared, "cgroup.procs"),
		[]byte(strconv.Itoa(neighbour.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewProcessService(set, "lonely")
	svc.Record().SetKillMode(KillModeControlGroup)
	svc.Record().SetDelegate(true, nil)
	if got := svc.OwnCgroupPath(); got != "" {
		t.Errorf("OwnCgroupPath = %q for a service on the shared default cgroup", got)
	}

	exited := make(chan error, 1)
	go func() { exited <- neighbour.Wait() }()
	svc.killCgroupTree(syscall.SIGTERM)
	select {
	case err := <-exited:
		t.Errorf("the neighbour in the shared cgroup was killed: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	var params process.ExecParams
	svc.Record().ApplyProcessAttrs(&params)
	if params.Delegate {
		t.Errorf("delegate = yes would hand over the shared cgroup %q", params.CgroupPath)
	}

	// With a cgroup of its own, both apply.
	own := filepath.Join(t.TempDir(), "own")
	svc.Record().SetCgroupPath(own)
	if got := svc.OwnCgroupPath(); got != own {
		t.Errorf("OwnCgroupPath = %q, want %q", got, own)
	}
	params = process.ExecParams{}
	svc.Record().ApplyProcessAttrs(&params)
	if !params.Delegate {
		t.Error("delegate dropped for a service with its own cgroup")
	}
}
