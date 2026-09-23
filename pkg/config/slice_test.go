package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

type sliceTestLogger struct{}

func (sliceTestLogger) ServiceStarted(string)        {}
func (sliceTestLogger) ServiceStopped(string)        {}
func (sliceTestLogger) ServiceFailed(string, bool)   {}
func (sliceTestLogger) Error(string, ...interface{}) {}
func (sliceTestLogger) Info(string, ...interface{})  {}

func loadSliceService(t *testing.T, name, content string) service.Service {
	t.Helper()
	dir := t.TempDir()
	ss := service.NewServiceSet(sliceTestLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write service file: %v", err)
	}
	svc, err := loader.LoadService(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return svc
}

// TestSliceWithoutCgroupPath is the loader-level counterpart to
// service.TestSliceEffectiveCgroupPath, which exercises the record
// setters directly and so cannot see this: the loader used to apply
// `slice` only inside the branch guarded by a non-empty `cgroup`, so a
// service that set `slice` alone silently ran in the daemon's default
// cgroup with none of the intended grouping. slinit-service(5) documents
// slice as sufficient on its own ("cgroup or slice must be set").
func TestSliceWithoutCgroupPath(t *testing.T) {
	svc := loadSliceService(t, "worker", "type = process\ncommand = /bin/app\nslice = system.slice\n")

	want := "/sys/fs/cgroup/system.slice/worker"
	if got := svc.Record().EffectiveCgroupPath(); got != want {
		t.Errorf("EffectiveCgroupPath() = %q, want %q", got, want)
	}
}

// TestSliceWithCgroupPath keeps the documented precedence: an explicit
// cgroup path wins, and setting both is not an error.
func TestSliceWithCgroupPath(t *testing.T) {
	svc := loadSliceService(t, "worker",
		"type = process\ncommand = /bin/app\nslice = system.slice\ncgroup = /sys/fs/cgroup/custom/worker\n")

	want := "/sys/fs/cgroup/custom/worker"
	if got := svc.Record().EffectiveCgroupPath(); got != want {
		t.Errorf("EffectiveCgroupPath() = %q, want %q", got, want)
	}
}

// TestNoSliceNoCgroup pins the fall-through: neither directive means the
// daemon default, which is empty on a set nobody configured.
func TestNoSliceNoCgroup(t *testing.T) {
	svc := loadSliceService(t, "worker", "type = process\ncommand = /bin/app\n")

	if got := svc.Record().EffectiveCgroupPath(); got != "" {
		t.Errorf("EffectiveCgroupPath() = %q, want empty", got)
	}
}
