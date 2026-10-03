package service

import (
	"errors"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// noteStartExecFailure records a start that never produced a process.
//
// StartProcess returns an *process.ExecError carrying the stage it failed
// at and the underlying errno, and every caller used to log it and throw
// it away. The service was then left with an empty ExitStatus and a stop
// reason still reading "normal" — so `slinitctl status` reported
// "Stopped reason: normal, si_code: 0" for a service whose command did
// not exist, which is the single most common way a start fails.
//
// Caller holds queueMu.
func noteStartExecFailure(rec *ServiceRecord, err error) ExitStatus {
	stage := uint8(process.StageDoExec)
	var errno int32
	var ee *process.ExecError
	if errors.As(err, &ee) {
		stage = uint8(ee.Stage)
		errno = extractErrno(ee.Err)
	} else {
		errno = extractErrno(err)
	}
	// ReasonExecFailed is defined as "failed to start (couldn't launch
	// process)", which is exactly this. It was only ever set for a
	// failure that came back as a child exit, i.e. one detected after the
	// fork.
	rec.stopReason = ReasonExecFailed
	return ExecFailureStatus(stage, errno)
}
