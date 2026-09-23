package service

import "testing"

// withRecordedCgroupRemovals swaps in a recorder for process.RemoveCgroup
// so these tests can watch the call without a writable /sys/fs/cgroup.
func withRecordedCgroupRemovals(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	old := removeCgroupFunc
	removeCgroupFunc = func(path string) error {
		seen = append(seen, path)
		return nil
	}
	t.Cleanup(func() { removeCgroupFunc = old })
	return &seen
}

// TestStoppedReclaimsSliceCgroup covers the case that leaked: a
// transient unit gets its cgroup from `slice`, its name is never reused,
// and nothing used to remove the directory — so one accumulated per
// `slinitctl run --slice=NAME`, surviving soft reboots.
func TestStoppedReclaimsSliceCgroup(t *testing.T) {
	seen := withRecordedCgroupRemovals(t)

	set, _ := newTestSet()
	svc := NewInternalService(set, "run-4a696d93")
	set.AddService(svc)
	rec := svc.Record()
	rec.SetSlice("slinit")

	rec.Stopped()

	want := "/sys/fs/cgroup/slinit/run-4a696d93"
	if len(*seen) != 1 || (*seen)[0] != want {
		t.Errorf("removals = %v, want exactly [%s]", *seen, want)
	}
}

// TestStoppedReclaimsExplicitCgroup: an explicitly configured path is
// still one slinit created (applyCgroup MkdirAll's it), so it is
// reclaimed on the same terms.
func TestStoppedReclaimsExplicitCgroup(t *testing.T) {
	seen := withRecordedCgroupRemovals(t)

	set, _ := newTestSet()
	svc := NewInternalService(set, "cgroup-demo")
	set.AddService(svc)
	rec := svc.Record()
	rec.SetCgroupPath("/sys/fs/cgroup/slinit/cgroup-demo")

	rec.Stopped()

	want := "/sys/fs/cgroup/slinit/cgroup-demo"
	if len(*seen) != 1 || (*seen)[0] != want {
		t.Errorf("removals = %v, want exactly [%s]", *seen, want)
	}
}

// TestStoppedLeavesDefaultCgroupAlone is the one that must not regress.
// With neither directive the effective path is the daemon-wide default,
// shared by every other unconfigured service — removing it when any one
// of them stops would pull the floor out from under the rest.
func TestStoppedLeavesDefaultCgroupAlone(t *testing.T) {
	seen := withRecordedCgroupRemovals(t)

	set, _ := newTestSet()
	set.SetDefaultCgroupPath("/sys/fs/cgroup/slinit")
	svc := NewInternalService(set, "plain")
	set.AddService(svc)

	svc.Record().Stopped()

	if len(*seen) != 0 {
		t.Errorf("removals = %v, want none for a service on the shared default", *seen)
	}
}
