package service

import (
	"syscall"
	"testing"
)

// A stop signal that fails with ESRCH means the process was already gone —
// a service that exits on its own the moment a stop begins (a pipe consumer
// whose writers stopped, for one) or one reaped just before we signalled it.
// That is a clean stop, so it must not reach the error log; any other
// failure still must.
func TestStopSignalESRCHIsNotAnError(t *testing.T) {
	set, logger := newTestSet()
	svc := NewInternalService(set, "pipe-consumer")
	set.AddService(svc)
	rec := svc.Record()

	rec.logStopSignalFailure(syscall.ESRCH)
	if len(logger.errors) != 0 {
		t.Errorf("ESRCH on a stop signal was logged as an error: %v", logger.errors)
	}

	rec.logStopSignalFailure(syscall.EPERM)
	if len(logger.errors) != 1 {
		t.Fatalf("a non-ESRCH signal failure should log exactly one error, got %d", len(logger.errors))
	}
}
