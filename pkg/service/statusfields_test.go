package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The audit behind these tests: ExitStatus's accessors are careful —
// Exited, Signaled and ExitCode all consult HasStatus — but the places
// that FILL the struct were not, and a decision downstream takes an empty
// one for fact. ScriptedService never filled it at all.

func waitState(t *testing.T, svc Service, want ServiceState, within time.Duration) ServiceState {
	t.Helper()
	deadline := time.Now().Add(within)
	var got ServiceState
	for time.Now().Before(deadline) {
		got = svc.State()
		if got == want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return got
}

func newScripted(t *testing.T, set *ServiceSet, name string, start, stop []string) *ScriptedService {
	t.Helper()
	svc := NewScriptedService(set, name)
	svc.SetStartCommand(start)
	svc.SetStopCommand(stop)
	set.AddService(svc)
	return svc
}

// A scripted service's start command exits 0: the status has to say so.
// It used to report HasStatus=false, so `slinitctl status` showed neither
// an exit code nor a meaningful si_code for a service that had just run.
func TestScriptedRecordsASuccessfulStart(t *testing.T) {
	set, _ := newTestSet()
	svc := newScripted(t, set, "scripted-ok", []string{"/bin/true"}, []string{"/bin/true"})

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}

	es := svc.GetExitStatus()
	if !es.HasStatus {
		t.Fatal("no status recorded for a scripted service that ran its start command")
	}
	if !es.Exited() || es.ExitCode() != 0 {
		t.Errorf("exited=%v code=%d, want a clean exit 0", es.Exited(), es.ExitCode())
	}
	if es.ExecFailed {
		t.Error("ExecFailed set for a command that ran")
	}
}

// And a failing one: the code the operator needs is the one the daemon
// already logs, and it now reaches the status too.
func TestScriptedRecordsAFailedStart(t *testing.T) {
	set, _ := newTestSet()
	script := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0755); err != nil {
		t.Fatal(err)
	}
	svc := newScripted(t, set, "scripted-fail", []string{script}, []string{"/bin/true"})

	set.StartService(svc)
	if got := waitState(t, svc, StateStopped, 5*time.Second); got != StateStopped {
		t.Fatalf("state = %v, want STOPPED", got)
	}

	es := svc.GetExitStatus()
	if !es.HasStatus {
		t.Fatal("no status recorded for a start command that failed")
	}
	if !es.Exited() || es.ExitCode() != 3 {
		t.Errorf("exited=%v code=%d, want exit 3", es.Exited(), es.ExitCode())
	}
}

// Cleared at the top of every start. The restart policy depends on this
// invariant — ExitStatus.Vanished's comment spells out why — so a status
// left over from the previous cycle must not be visible during the next
// start attempt.
func TestScriptedClearsItsStatusOnRestart(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	script := filepath.Join(dir, "once.sh")
	// Fails the first time, succeeds after: exit 3, then touch a marker
	// so the second run takes the other branch.
	body := "#!/bin/sh\nif [ -e " + dir + "/ran ]; then exit 0; fi\ntouch " + dir + "/ran\nexit 3\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	svc := newScripted(t, set, "scripted-twice", []string{script}, []string{"/bin/true"})

	set.StartService(svc)
	if got := waitState(t, svc, StateStopped, 5*time.Second); got != StateStopped {
		t.Fatalf("first attempt: state = %v, want STOPPED", got)
	}
	if code := svc.GetExitStatus().ExitCode(); code != 3 {
		t.Fatalf("first attempt: exit code %d, want 3", code)
	}

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("second attempt: state = %v, want STARTED", got)
	}
	if es := svc.GetExitStatus(); !es.Exited() || es.ExitCode() != 0 {
		t.Errorf("second attempt reports code %d — the previous cycle's status survived",
			es.ExitCode())
	}
}

// `normal-exit` declares codes that count as success and must suppress a
// respawn. It is evaluated from the exit status, so for a scripted
// service it could never match: IsNormalExit was being handed an empty
// struct on every call.
func TestNormalExitMatchesForAScriptedService(t *testing.T) {
	set, _ := newTestSet()
	script := filepath.Join(t.TempDir(), "code3.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0755); err != nil {
		t.Fatal(err)
	}
	svc := newScripted(t, set, "scripted-normal", []string{script}, []string{"/bin/true"})
	svc.Record().SetNormalExitCodes([]int{3})

	set.StartService(svc)
	if got := waitState(t, svc, StateStopped, 5*time.Second); got != StateStopped {
		t.Fatalf("state = %v, want STOPPED", got)
	}

	if !svc.Record().IsNormalExit(svc.GetExitStatus()) {
		t.Errorf("exit 3 with `normal-exit = 3` is not recognised as normal; status was %+v",
			svc.GetExitStatus())
	}
}

// A command that does not exist is the most common way a start fails,
// and StartProcess reports it synchronously rather than as a child exit.
// Every caller logged that error and dropped it, so the service was left
// with an empty ExitStatus and a stop reason still reading "normal" — a
// service whose binary was missing reported itself stopped *normally*.
//
// Measured before the fix: reason=normal, hasStatus=false,
// execFailed=false, stage=0, errno=0. After: reason=exec-failed,
// stage=13 (do-exec), errno=2 (ENOENT) for a missing file and 13
// (EACCES) for one that is not executable.
func TestSynchronousExecFailureIsRecorded(t *testing.T) {
	for _, tc := range []struct {
		name string
		mk   func(*ServiceSet) Service
	}{
		{"process", func(s *ServiceSet) Service {
			svc := NewProcessService(s, "exec-fail-proc")
			svc.SetCommand([]string{"/nonexistent/binary"})
			return svc
		}},
		{"scripted", func(s *ServiceSet) Service {
			svc := NewScriptedService(s, "exec-fail-scripted")
			svc.SetStartCommand([]string{"/nonexistent/binary"})
			svc.SetStopCommand([]string{"/bin/true"})
			return svc
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := newTestSet()
			svc := tc.mk(set)
			set.AddService(svc)

			set.StartService(svc)
			if got := waitState(t, svc, StateStopped, 5*time.Second); got != StateStopped {
				t.Fatalf("state = %v, want STOPPED", got)
			}

			es := svc.GetExitStatus()
			if !es.ExecFailed {
				t.Fatalf("ExecFailed not set after exec of a missing binary; status %+v", es)
			}
			if es.HasStatus {
				t.Error("HasStatus set for a process that never ran — a zero " +
					"WaitStatus reads as a clean exit 0, which is how a failed " +
					"start would come to look successful")
			}
			if es.ExecErrno != 2 { // ENOENT
				t.Errorf("errno = %d, want 2 (ENOENT) for a missing binary", es.ExecErrno)
			}
			if r := svc.Record().StopReason(); r != ReasonExecFailed {
				t.Errorf("stop reason is %v, want exec-failed; a service whose "+
					"command does not exist did not stop normally", r)
			}
		})
	}
}
