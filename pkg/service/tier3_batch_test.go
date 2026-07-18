package service

import (
	"testing"
	"time"
)

func TestJobTimeoutSetterAndDefault(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "jt")
	rec := svc.Record()
	if rec.JobTimeout() != 0 {
		t.Errorf("default job-timeout should be 0, got %v", rec.JobTimeout())
	}
	rec.SetJobTimeout(2 * time.Minute)
	if rec.JobTimeout() != 2*time.Minute {
		t.Errorf("SetJobTimeout(2m) not stored: %v", rec.JobTimeout())
	}
}

func TestSliceEffectiveCgroupPath(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "sliced")
	set.AddService(svc)
	rec := svc.Record()

	// No slice, no cgroup: falls back to daemon default (empty in test set).
	if got := rec.EffectiveCgroupPath(); got != "" {
		t.Errorf("no-slice no-cgroup: got %q, want empty", got)
	}

	// Slice set: prepended to service name under /sys/fs/cgroup.
	rec.SetSlice("system.slice")
	want := "/sys/fs/cgroup/system.slice/sliced"
	if got := rec.EffectiveCgroupPath(); got != want {
		t.Errorf("slice-only: got %q, want %q", got, want)
	}

	// Explicit cgroup path wins over slice (backward compat).
	rec.SetCgroupPath("/sys/fs/cgroup/custom/sliced")
	if got := rec.EffectiveCgroupPath(); got != "/sys/fs/cgroup/custom/sliced" {
		t.Errorf("explicit cgroup should win: got %q", got)
	}
}
