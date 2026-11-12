package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestOpenPSITriggerWritesSpec drives openPSITrigger against a
// tmpfile — verifies the trigger string ("some THRESHOLD_US
// WINDOW_US") is what the kernel would see. A real cgroup v2
// pressure file would return POLLPRI on the fd afterwards; our
// stand-in just captures the write.
func TestOpenPSITriggerWritesSpec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.pressure")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("prep: %v", err)
	}
	fd, err := openPSITrigger(path, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	unix.Close(fd)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want := "some 150000 2000000"
	if string(got) != want {
		t.Errorf("trigger spec: got %q want %q", got, want)
	}
}

// TestOpenPSITriggerClampsThreshold verifies the [500us, window]
// clamp. A sub-500us threshold is rounded up to 500us; a threshold
// larger than the 2s window is clamped down to the window (kernel
// would reject either bound).
func TestOpenPSITriggerClampsThreshold(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{100 * time.Microsecond, "some 500 2000000"},
		{5 * time.Second, "some 2000000 2000000"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "cpu.pressure")
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatalf("prep: %v", err)
		}
		fd, err := openPSITrigger(path, c.in)
		if err != nil {
			t.Fatalf("in=%v: open: %v", c.in, err)
		}
		unix.Close(fd)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(got) != c.want {
			t.Errorf("in=%v: got %q want %q", c.in, got, c.want)
		}
	}
}

// TestOpenPSITriggerMissingFile returns an error rather than
// panicking. In production this fires on kernels without PSI (no
// {memory,cpu,io}.pressure file under the cgroup) — the caller
// logs and skips that resource without failing the whole start.
func TestOpenPSITriggerMissingFile(t *testing.T) {
	_, err := openPSITrigger("/nonexistent/memory.pressure", 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected error opening a missing pressure file")
	}
}

// TestSetPSIWatchers pins the ServiceRecord setter surface. The
// loader wires all three (mem/cpu/io) unconditionally; the watcher
// arm decides which to actually open based on the enabled flag.
func TestSetPSIWatchers(t *testing.T) {
	sr := &ServiceRecord{}
	sr.SetPSIMemoryWatch(true, 300*time.Millisecond)
	sr.SetPSICPUWatch(true, 400*time.Millisecond)
	sr.SetPSIIOWatch(true, 500*time.Millisecond)
	if !sr.psiMemWatch || sr.psiMemThr != 300*time.Millisecond {
		t.Errorf("memory: got (%v,%v)", sr.psiMemWatch, sr.psiMemThr)
	}
	if !sr.psiCPUWatch || sr.psiCPUThr != 400*time.Millisecond {
		t.Errorf("cpu: got (%v,%v)", sr.psiCPUWatch, sr.psiCPUThr)
	}
	if !sr.psiIOWatch || sr.psiIOThr != 500*time.Millisecond {
		t.Errorf("io: got (%v,%v)", sr.psiIOWatch, sr.psiIOThr)
	}
}

// psiOpenFds counts this process's fds open on path.
func psiOpenFds(t *testing.T, path string) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	n := 0
	for _, e := range ents {
		if l, _ := os.Readlink("/proc/self/fd/" + e.Name()); l == path {
			n++
		}
	}
	return n
}

// Cancelling must close the trigger at once. The goroutine used to notice
// only on a 500ms poll timeout, so the trigger outlived the cgroup's rmdir,
// and destroying it there cost every stop a kernel synchronize_rcu.
func TestPSIWatcherCancelClosesTriggerAtOnce(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	pressure := filepath.Join(dir, "memory.pressure")
	if err := os.WriteFile(pressure, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewInternalService(set, "psi-cancel")
	sr := svc.Record()
	sr.SetCgroupPath(dir)
	sr.SetPSIMemoryWatch(true, 0)
	sr.armPSIWatcher()
	if sr.psiWatch == nil || psiOpenFds(t, pressure) != 1 {
		t.Fatalf("watcher not armed (open fds on the trigger: %d)", psiOpenFds(t, pressure))
	}
	began := time.Now()
	sr.cancelPSIWatcher()
	for psiOpenFds(t, pressure) != 0 {
		if time.Since(began) > 200*time.Millisecond {
			t.Fatal("trigger fd still open 200ms after cancel")
		}
		time.Sleep(time.Millisecond)
	}
	sr.cancelPSIWatcher() // a second cancel is a no-op
}

// A stop has to cancel the watcher when it begins, not in Stopped(): the
// rmdir there waits for a trigger teardown still in progress.
func TestPSIWatcherCancelledWhenStopBegins(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.pressure"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewInternalService(set, "psi-stop")
	sr := svc.Record()
	sr.SetCgroupPath(dir)
	sr.SetPSIMemoryWatch(true, 0)
	set.AddService(svc)
	set.StartService(svc)
	sr.armPSIWatcher()
	if sr.psiWatch == nil {
		t.Fatal("watcher not armed")
	}
	set.queueMu.Lock()
	sr.doStop(false)
	stillArmed := sr.psiWatch != nil
	set.queueMu.Unlock()
	if stillArmed {
		t.Fatal("watcher still armed after the stop began")
	}
}
