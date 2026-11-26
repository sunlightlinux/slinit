package service

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
	"golang.org/x/sys/unix"
)

// reexecWithPrivateRun re-runs the calling test as root in a new user
// and mount namespace with a tmpfs on /run (the fd-store's notify
// socket lives under /run/slinit), and reports that run. It returns
// true in the re-run, where the test body belongs.
func reexecWithPrivateRun(t *testing.T) bool {
	t.Helper()
	if os.Getenv("SLINIT_TEST_MOUNTNS") == "1" {
		if err := unix.Mount("tmpfs", "/run", "tmpfs", 0, ""); err != nil {
			t.Skipf("mount tmpfs on /run: %v", err)
		}
		return true
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare not available")
	}
	if exec.Command(unshare, "--user", "--map-root-user", "--mount", "true").Run() != nil {
		t.Skip("unshare --user --map-root-user --mount not permitted here")
	}
	cmd := exec.Command(unshare, "--user", "--map-root-user", "--mount", "--propagation", "private",
		os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), "SLINIT_TEST_MOUNTNS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("in mount namespace: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("skipped in mount namespace:\n%s", out)
	}
	return false
}

// The fd-store is drained into each new process's LISTEN_FDS, and the
// child re-stores what it wants to keep. PID 1's own copies of the
// drained files were never closed: each restart of an fd-store service
// left them open until a garbage collection ran their finalizers, and
// PID 1 allocates little. Measured on a user instance: 2 stored pipes,
// 40 restarts, 81 pipe fds open in slinit, back to 5 after a forced GC.
//
// Here the read end of a pipe is stored and the service started once,
// then stopped. Once the child is gone, nothing should hold the read
// end, so a write fails with EPIPE; it succeeded while slinit kept its
// copy. The test keeps its *os.File reachable so no finalizer can close
// the copy behind its back.
func TestFDStoreHandoffClosesParentCopies(t *testing.T) {
	if !reexecWithPrivateRun(t) {
		return
	}
	set, _ := newTestSet()
	svc := NewProcessService(set, "fdhandoff")
	svc.SetCommand([]string{"/bin/sleep", "30"})
	set.AddService(svc)
	svc.Record().SetFDStoreMax(2)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := svc.Record().FDStore().Add(process.FDStoreEntry{Name: "p", File: r}); err != nil {
		t.Fatal(err)
	}

	set.StartService(svc)
	if st := waitState(t, svc, StateStarted, 5*time.Second); st != StateStarted {
		t.Fatalf("state %v, want STARTED", st)
	}
	set.StopService(svc)
	if st := waitState(t, svc, StateStopped, 5*time.Second); st != StateStopped {
		t.Fatalf("state %v, want STOPPED", st)
	}

	if _, err := w.Write([]byte("x")); !errors.Is(err, syscall.EPIPE) {
		t.Errorf("write to the stored pipe after the service stopped: %v, want EPIPE "+
			"(slinit still holds the read end it handed to the child)", err)
	}
	runtime.KeepAlive(r)
}
