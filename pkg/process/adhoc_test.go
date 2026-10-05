package process

import (
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// reaperLoop mimics eventloop.reapOrphans: Wait4(-1, WNOHANG) over and
// over, collecting whatever zombie is available — including a child that
// os/exec is in the middle of waiting on — and handing each status to
// DefaultExitRouter.Route. Returns a stop function.
//
// Routing is not decoration: it is the half of the contract that makes
// the helpers work at all. A reaper that collected statuses without
// routing them would leave every stolen child's status nowhere, and
// these tests would (correctly) fail. Keep this in step with
// eventloop.reapOrphans.
//
// The real daemon only reaps on SIGCHLD, so the window is far narrower
// there; a tight loop turns a race that costs one functional case in
// seven into one that is near-certain, which is what a regression test
// needs.
func reaperLoop() func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid > 0 && err == nil {
				DefaultExitRouter.Route(pid, ws)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// Establishes the hazard the helpers exist for: with a PID-1-style
// reaper running, plain cmd.Output() reports a failure for a command
// that ran perfectly, because its child was reaped out from under
// os/exec and the wait returns ECHILD. This is not a test of our code —
// it is the reason the rest of this file exists, and it fails loudly if
// a future Go release makes os/exec immune (at which point the helpers
// could go).
func TestPlainExecLosesChildrenToTheReaper(t *testing.T) {
	stop := reaperLoop()
	defer stop()

	lost := 0
	var sample error
	for i := 0; i < 400; i++ {
		if _, err := exec.Command("/bin/echo", "K=V").Output(); err != nil {
			lost++
			sample = err
		}
	}
	if lost == 0 {
		t.Skip("os/exec no longer loses children to a Wait4(-1) reaper on " +
			"this Go/kernel; the adhoc helpers may no longer be needed")
	}
	t.Logf("plain cmd.Output() lost %d of 400 children; e.g. %v", lost, sample)
}

// OutputAdhoc must return the command's output and no error under the
// same reaper. This is the shape that broke functional case
// 163-env-generator: runEnvGenerator used cmd.Output(), threw the
// perfectly good stdout away on the phantom error, and the service came
// up without the generated variables.
func TestOutputAdhocSurvivesTheReaper(t *testing.T) {
	stop := reaperLoop()
	defer stop()

	const want = "SLINIT_EG_TEST=eg-value-42"
	bad := 0
	for i := 0; i < 400; i++ {
		out, err := OutputAdhoc(exec.Command("/bin/echo", want))
		if err != nil {
			t.Errorf("iteration %d: OutputAdhoc: %v", i, err)
			bad++
		} else if strings.TrimSpace(string(out)) != want {
			t.Errorf("iteration %d: output = %q, want %q", i, out, want)
			bad++
		}
		if bad > 3 {
			t.Fatal("too many failures; stopping")
		}
	}
}

// A real non-zero exit must still be reported as one, whichever of
// cmd.Wait() and the router produced the status — otherwise the fix
// would trade phantom failures for phantom successes, which is worse.
func TestRunAdhocReportsRealExitCodeUnderTheReaper(t *testing.T) {
	stop := reaperLoop()
	defer stop()

	for i := 0; i < 200; i++ {
		err := RunAdhoc(exec.Command("/bin/sh", "-c", "exit 7"))
		if err == nil {
			t.Fatalf("iteration %d: exit 7 reported as success", i)
		}
		code, ok := ExitCodeOf(err)
		if !ok {
			t.Fatalf("iteration %d: no exit status in %v", i, err)
		}
		if code != 7 {
			t.Fatalf("iteration %d: exit code = %d, want 7 (%v)", i, code, err)
		}
	}
}

// And a clean exit must stay clean.
func TestRunAdhocReportsSuccessUnderTheReaper(t *testing.T) {
	stop := reaperLoop()
	defer stop()

	for i := 0; i < 200; i++ {
		if err := RunAdhoc(exec.Command("/bin/true")); err != nil {
			t.Fatalf("iteration %d: /bin/true reported %v", i, err)
		}
	}
}

// Concurrent callers must not cross statuses: each gets its own child's
// code even when the reaper is taking some of them.
func TestRunAdhocConcurrentStatusesDoNotCross(t *testing.T) {
	stop := reaperLoop()
	defer stop()

	var wg sync.WaitGroup
	for w := 1; w <= 8; w++ {
		wg.Add(1)
		go func(code int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				err := RunAdhoc(exec.Command("/bin/sh", "-c",
					"exit "+string(rune('0'+code))))
				got, ok := ExitCodeOf(err)
				if !ok {
					t.Errorf("worker %d: no status in %v", code, err)
					return
				}
				if got != code {
					t.Errorf("worker %d: got exit %d", code, got)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}

// ExitCodeOf must refuse to invent a code for an error that carries
// none — an exec failure is not "exited 0".
func TestExitCodeOfRefusesToInventAStatus(t *testing.T) {
	_, err := OutputAdhoc(exec.Command("/nonexistent/binary-for-slinit-test"))
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if code, ok := ExitCodeOf(err); ok {
		t.Errorf("ExitCodeOf reported code %d for an exec failure; want ok=false", code)
	}
}
