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

// Delegation has to survive the loader, not just the parser: `slice` once
// did not (see TestSliceWithoutCgroupPath above), so the same end-to-end
// check is worth having for its neighbour.
func TestDelegateReachesTheRecord(t *testing.T) {
	cases := []struct {
		name        string
		directive   string
		wantOn      bool
		wantControl []string
	}{
		{"yes", "delegate = yes\n", true, nil},
		{"no", "delegate = no\n", false, nil},
		{"absent", "", false, nil},
		{"controller list", "delegate = memory pids\n", true, []string{"memory", "pids"}},
		{"comma separated", "delegate = memory,io\n", true, []string{"memory", "io"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := loadSliceService(t, "payload",
				"type = process\ncommand = /bin/app\nslice = system.slice\n"+tc.directive)
			rec, ok := svc.(interface {
				Delegate() bool
				DelegateControllers() []string
			})
			if !ok {
				t.Fatal("service does not expose the delegation accessors")
			}
			if rec.Delegate() != tc.wantOn {
				t.Errorf("Delegate() = %v, want %v", rec.Delegate(), tc.wantOn)
			}
			got := rec.DelegateControllers()
			if len(got) != len(tc.wantControl) {
				t.Fatalf("controllers = %v, want %v", got, tc.wantControl)
			}
			for i := range got {
				if got[i] != tc.wantControl[i] {
					t.Errorf("controllers = %v, want %v", got, tc.wantControl)
				}
			}
		})
	}
}

func TestDelegateRejectsNonsense(t *testing.T) {
	// "-memory" would reach a write to cgroup.subtree_control and disable
	// a controller rather than delegate one. A fresh loader per case so a
	// rejected load cannot leave state behind for the next.
	for _, bad := range []string{"-memory", "+memory", "Memory", "mem/ory"} {
		dir := t.TempDir()
		ss := service.NewServiceSet(sliceTestLogger{})
		loader := NewDirLoader(ss, []string{dir})
		ss.SetLoader(loader)
		if err := os.WriteFile(filepath.Join(dir, "bad"),
			[]byte("type = process\ncommand = /bin/app\ndelegate = "+bad+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := loader.LoadService("bad"); err == nil {
			t.Errorf("delegate = %q was accepted", bad)
		}
	}
}

// systemd writes the slice hierarchy into the name with dashes; slinit
// takes the path. The mapping used to warn "slinit cgroup grouping
// differs; review manually" and drop the value, so a unit that said
// Slice= silently lost its grouping — while slinit's own `slice=` is
// systemd-style by construction and only the naming convention differed.
func TestExpandSystemdSlice(t *testing.T) {
	cases := map[string]string{
		"system.slice":     "system.slice",
		"user-1000.slice":  "user.slice/user-1000.slice",
		"machine.slice":    "machine.slice",
		"a-b-c.slice":      "a.slice/a-b.slice/a-b-c.slice",
		"system":           "system.slice",
		" system.slice ":   "system.slice",
		"\"system.slice\"": "system.slice",
		"-.slice":          "", // systemd's name for the root: no grouping
		"":                 "",
		".slice":           "",
	}
	for in, want := range cases {
		if got := expandSystemdSlice(in); got != want {
			t.Errorf("expandSystemdSlice(%q) = %q, want %q", in, got, want)
		}
	}
}
