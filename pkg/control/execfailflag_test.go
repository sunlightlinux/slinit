package control

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Which of the two status layouts is on the wire — si_code/si_status, or
// an exec stage and errno — could not be read off the payload. A client
// had to infer it from the stage being nonzero, and StageArrangeFDs is 0,
// so a failure in the first stage read as an ordinary exit and its errno
// was rendered as an si_code. StatusFlagExecFailed says it outright.
func TestExecFailedFlagMarksTheStageErrnoLayout(t *testing.T) {
	set := service.NewServiceSet(&flagTestLogger{})

	svc := service.NewProcessService(set, "exec-fail-svc")
	svc.SetCommand([]string{"/nonexistent/binary"})
	set.AddService(svc)
	set.StartService(svc)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && svc.State() != service.StateStopped {
		time.Sleep(10 * time.Millisecond)
	}

	status, err := DecodeServiceStatus5(EncodeServiceStatus5(svc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Flags&StatusFlagExecFailed == 0 {
		t.Fatal("StatusFlagExecFailed not set for a service whose exec failed")
	}
	// The payload now says stage + errno, and the errno rides in the
	// field a client would otherwise read as si_code.
	if status.SiCode != 2 { // ENOENT
		t.Errorf("errno field = %d, want 2 (ENOENT)", status.SiCode)
	}
	if status.StopReason != uint8(service.ReasonExecFailed) {
		t.Errorf("stop reason = %d, want %d (exec-failed)",
			status.StopReason, uint8(service.ReasonExecFailed))
	}
}

// And a service that ran and exited must not carry the flag, or every
// ordinary exit would be rendered as an exec failure.
func TestExecFailedFlagAbsentForANormalExit(t *testing.T) {
	set := service.NewServiceSet(&flagTestLogger{})

	svc := service.NewProcessService(set, "clean-exit-svc")
	svc.SetCommand([]string{"/bin/true"})
	set.AddService(svc)
	set.StartService(svc)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && svc.State() != service.StateStopped {
		time.Sleep(10 * time.Millisecond)
	}

	status, err := DecodeServiceStatus5(EncodeServiceStatus5(svc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Flags&StatusFlagExecFailed != 0 {
		t.Error("StatusFlagExecFailed set for a process that ran and exited")
	}
	if status.SiCode != 1 { // CLD_EXITED
		t.Errorf("si_code = %d, want 1 (CLD_EXITED)", status.SiCode)
	}

	// The v1 status carries the code itself, and zero is a real code:
	// the client's guard used to be `> 0`, which hid every clean exit.
	v1, err := DecodeServiceStatus(EncodeServiceStatus(svc))
	if err != nil {
		t.Fatalf("decode v1: %v", err)
	}
	if v1.ExitStatus != 0 {
		t.Errorf("exit status = %d, want 0 for /bin/true", v1.ExitStatus)
	}
}

type flagTestLogger struct{}

func (l *flagTestLogger) ServiceStarted(string)        {}
func (l *flagTestLogger) ServiceStopped(string)        {}
func (l *flagTestLogger) ServiceFailed(string, bool)   {}
func (l *flagTestLogger) Error(string, ...interface{}) {}
func (l *flagTestLogger) Info(string, ...interface{})  {}
