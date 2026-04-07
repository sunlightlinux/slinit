package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// testServiceLogger implements service.ServiceLogger for tests.
type testServiceLogger struct{}

func (l *testServiceLogger) ServiceStarted(name string)              {}
func (l *testServiceLogger) ServiceStopped(name string)              {}
func (l *testServiceLogger) ServiceFailed(name string, dep bool)     {}
func (l *testServiceLogger) Error(format string, args ...interface{}) {}
func (l *testServiceLogger) Info(format string, args ...interface{})  {}

func writeNSServiceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write service file %s: %v", name, err)
	}
}

func TestLoaderNamespaceCloneflags(t *testing.T) {
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-ns", `type = process
command = /bin/true
namespace-pid = true
namespace-mount = true
namespace-net = true
namespace-uts = true
namespace-ipc = true
namespace-user = true
namespace-cgroup = true
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("test-ns")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	rec := svc.Record()
	var params process.ExecParams
	rec.ApplyProcessAttrs(&params)

	expected := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET |
		syscall.CLONE_NEWUTS | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUSER | syscall.CLONE_NEWCGROUP)

	if params.Cloneflags != expected {
		t.Errorf("Cloneflags = %#x, want %#x", params.Cloneflags, expected)
	}
}

func TestLoaderNamespacePartial(t *testing.T) {
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-partial", `type = process
command = /bin/true
namespace-pid = true
namespace-mount = true
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("test-partial")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	var params process.ExecParams
	svc.Record().ApplyProcessAttrs(&params)

	expected := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNS)
	if params.Cloneflags != expected {
		t.Errorf("Cloneflags = %#x, want %#x", params.Cloneflags, expected)
	}
}

func TestLoaderNamespaceNone(t *testing.T) {
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-none", `type = process
command = /bin/true
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("test-none")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	var params process.ExecParams
	svc.Record().ApplyProcessAttrs(&params)

	if params.Cloneflags != 0 {
		t.Errorf("Cloneflags = %#x, want 0", params.Cloneflags)
	}
}

func TestLoaderUidGidMappings(t *testing.T) {
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-uidgid", `type = process
command = /bin/true
namespace-user = true
namespace-uid-map = 0:1000:65536
namespace-gid-map = 0:1000:65536
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("test-uidgid")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	var params process.ExecParams
	svc.Record().ApplyProcessAttrs(&params)

	if len(params.UidMappings) != 1 {
		t.Fatalf("UidMappings count = %d, want 1", len(params.UidMappings))
	}
	if params.UidMappings[0].ContainerID != 0 || params.UidMappings[0].HostID != 1000 || params.UidMappings[0].Size != 65536 {
		t.Errorf("UidMappings[0] = %+v, want {0 1000 65536}", params.UidMappings[0])
	}

	if len(params.GidMappings) != 1 {
		t.Fatalf("GidMappings count = %d, want 1", len(params.GidMappings))
	}
	if params.GidMappings[0].ContainerID != 0 || params.GidMappings[0].HostID != 1000 || params.GidMappings[0].Size != 65536 {
		t.Errorf("GidMappings[0] = %+v, want {0 1000 65536}", params.GidMappings[0])
	}
}

func TestLoaderMultipleUidMappings(t *testing.T) {
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-multi", `type = process
command = /bin/true
namespace-user = true
namespace-uid-map = 0:1000:1000
namespace-uid-map += 1000:2000:1000
namespace-gid-map = 0:1000:65536
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("test-multi")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	var params process.ExecParams
	svc.Record().ApplyProcessAttrs(&params)

	if len(params.UidMappings) != 2 {
		t.Fatalf("UidMappings count = %d, want 2", len(params.UidMappings))
	}
	if params.UidMappings[0].ContainerID != 0 || params.UidMappings[0].HostID != 1000 {
		t.Errorf("UidMappings[0] = %+v", params.UidMappings[0])
	}
	if params.UidMappings[1].ContainerID != 1000 || params.UidMappings[1].HostID != 2000 {
		t.Errorf("UidMappings[1] = %+v", params.UidMappings[1])
	}
}

func TestLoaderNoNewPrivs(t *testing.T) {
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-nnp", `type = process
command = /bin/true
options = no-new-privs
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("test-nnp")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	var params process.ExecParams
	svc.Record().ApplyProcessAttrs(&params)

	if !params.NoNewPrivs {
		t.Error("NoNewPrivs should be true")
	}
}

func TestLoaderChrootLoads(t *testing.T) {
	// chroot is set on ProcessService, not ServiceRecord, so we just verify
	// that the config loads without error (wiring is in ProcessService.buildExecParams)
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-chroot", `type = process
command = /bin/true
chroot = /var/lib/myservice
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("test-chroot")
	if err != nil {
		t.Fatalf("LoadService with chroot should succeed: %v", err)
	}
}

func TestLoaderLockFileLoads(t *testing.T) {
	// lock-file is set on ProcessService, not ServiceRecord
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-lock", `type = process
command = /bin/true
lock-file = /run/myservice.lock
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("test-lock")
	if err != nil {
		t.Fatalf("LoadService with lock-file should succeed: %v", err)
	}
}

func TestLoaderCgroupPath(t *testing.T) {
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-cgroup", `type = process
command = /bin/true
cgroup = /sys/fs/cgroup/myservice
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("test-cgroup")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	var params process.ExecParams
	svc.Record().ApplyProcessAttrs(&params)

	if params.CgroupPath != "/sys/fs/cgroup/myservice" {
		t.Errorf("CgroupPath = %q, want %q", params.CgroupPath, "/sys/fs/cgroup/myservice")
	}
}

func TestLoaderIsolationCombo(t *testing.T) {
	// Full isolation combo: namespaces + no-new-privs + cgroup + new-session
	// chroot and lock-file are on ProcessService, tested via load success
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "test-combo", `type = process
command = /bin/true
namespace-pid = true
namespace-mount = true
namespace-user = true
namespace-uid-map = 0:1000:65536
namespace-gid-map = 0:1000:65536
options = no-new-privs
cgroup = /sys/fs/cgroup/isolated
`)

	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("test-combo")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	var params process.ExecParams
	svc.Record().ApplyProcessAttrs(&params)

	// Check isolation settings wired through ServiceRecord
	expectedFlags := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER)
	if params.Cloneflags != expectedFlags {
		t.Errorf("Cloneflags = %#x, want %#x", params.Cloneflags, expectedFlags)
	}
	if len(params.UidMappings) != 1 {
		t.Errorf("UidMappings = %d, want 1", len(params.UidMappings))
	}
	if len(params.GidMappings) != 1 {
		t.Errorf("GidMappings = %d, want 1", len(params.GidMappings))
	}
	if !params.NoNewPrivs {
		t.Error("NoNewPrivs should be true")
	}
	if params.CgroupPath != "/sys/fs/cgroup/isolated" {
		t.Errorf("CgroupPath = %q", params.CgroupPath)
	}
}
