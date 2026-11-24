package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A command run through OutputAdhoc that leaves a child in the
// background used to keep the caller waiting for as long as that child
// lived: the child holds the output pipe, and Wait waits for EOF on it.
// Hooks, run-action, env generators, the power hook and the OpenRC
// depend parser all run this way, several of them inside PID 1's
// boot, shutdown or scheduling paths.
func TestOutputAdhocDoesNotWaitForABackgroundChild(t *testing.T) {
	prev := AdhocOutputDelay
	AdhocOutputDelay = 300 * time.Millisecond
	t.Cleanup(func() { AdhocOutputDelay = prev })

	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidPath); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	type res struct {
		out []byte
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := OutputAdhoc(exec.Command("/bin/sh", "-c",
			"echo KEY=value; sleep 30 & echo $! > "+pidPath+"; exit 0"))
		done <- res{out, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Errorf("err = %v, want nil: the command exited 0", r.err)
		}
		if !strings.Contains(string(r.out), "KEY=value") {
			t.Errorf("output %q lost the command's own output", r.out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OutputAdhoc still waiting on a pipe held by a background child")
	}

	// A failing command still reports its failure.
	_, err := OutputAdhoc(exec.Command("/bin/sh", "-c", "sleep 3 & exit 3"))
	if code, ok := ExitCodeOf(err); !ok || code != 3 {
		t.Errorf("exit 3 with a background child: err = %v, want exit status 3", err)
	}
}
