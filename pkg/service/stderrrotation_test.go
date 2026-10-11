package service

import (
	"testing"
	"time"
)

// The override is what distinguishes this from stderr-logfile alone,
// which gave stderr its own file while rotatorConfigFor varied only the
// path — one policy, two paths.
func TestStderrRotationOverridesApply(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "err-rot")
	svc.SetLogRotation(10<<20, 8, 24*time.Hour)
	svc.SetStderrLogRotation(1<<20, 3, time.Hour)

	out := svc.rotatorConfigFor("/tmp/out.log")
	err := svc.rotatorConfigForStderr("/tmp/err.log")

	if out.MaxSize != 10<<20 || out.MaxFiles != 8 || out.RotateTime != 24*time.Hour {
		t.Errorf("stdout config changed: size=%d files=%d time=%v",
			out.MaxSize, out.MaxFiles, out.RotateTime)
	}
	if err.MaxSize != 1<<20 || err.MaxFiles != 3 || err.RotateTime != time.Hour {
		t.Errorf("stderr override not applied: size=%d files=%d time=%v",
			err.MaxSize, err.MaxFiles, err.RotateTime)
	}
}

// A zero in any position inherits that one setting, so a service can
// change only the size and keep the rest.
func TestStderrRotationPartialOverrideInherits(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "err-rot-partial")
	svc.SetLogRotation(10<<20, 8, 24*time.Hour)
	svc.SetStderrLogRotation(1<<20, 0, 0)

	cfg := svc.rotatorConfigForStderr("/tmp/err.log")
	if cfg.MaxSize != 1<<20 {
		t.Errorf("overridden size = %d, want %d", cfg.MaxSize, 1<<20)
	}
	if cfg.MaxFiles != 8 {
		t.Errorf("inherited files = %d, want 8", cfg.MaxFiles)
	}
	if cfg.RotateTime != 24*time.Hour {
		t.Errorf("inherited time = %v, want 24h", cfg.RotateTime)
	}
}

// With no override at all the two streams must still agree, which is
// the behaviour every existing stderr-logfile service relies on.
func TestStderrRotationNoOverrideMatchesStdout(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "err-rot-none")
	svc.SetLogRotation(10<<20, 8, 24*time.Hour)

	out := svc.rotatorConfigFor("/tmp/out.log")
	err := svc.rotatorConfigForStderr("/tmp/err.log")
	if out.MaxSize != err.MaxSize || out.MaxFiles != err.MaxFiles ||
		out.RotateTime != err.RotateTime {
		t.Errorf("streams diverged without an override: %d/%d/%v vs %d/%d/%v",
			out.MaxSize, out.MaxFiles, out.RotateTime,
			err.MaxSize, err.MaxFiles, err.RotateTime)
	}
	// And the journal unit name stays the bare service name on both,
	// which slinit-journalctl -u depends on.
	if out.ServiceName != "err-rot-none" || err.ServiceName != "err-rot-none" {
		t.Errorf("ServiceName drifted: %q / %q", out.ServiceName, err.ServiceName)
	}
}
