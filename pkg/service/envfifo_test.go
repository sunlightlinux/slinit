package service

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// startReturns starts svc and reports whether StartService came back
// within two seconds. StartService runs the start under the service
// set's lock, so a start stuck in it freezes every other start, stop
// and control-socket command with it. On a timeout the FIFOs in
// unblock are opened for writing so the test can finish.
func startReturns(t *testing.T, set *ServiceSet, svc Service, unblock ...string) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		set.StartService(svc)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(2 * time.Second):
		if set.queueMu.TryLock() {
			set.queueMu.Unlock()
		} else {
			t.Log("queueMu is held by the stuck start")
		}
		for _, p := range unblock {
			if w, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				w.Close()
			}
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return false
	}
}

// A FIFO left in an env-dir used to block the start in open(2) until
// something opened its write end, with the service set's lock held.
// The FIFO is skipped like any other unreadable entry; the regular
// file next to it still reaches the service.
func TestEnvDirFIFODoesNotBlockStart(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "PIPE")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PLAIN"), []byte("v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewProcessService(set, "envdir-fifo")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.SetEnvDir(dir)
	set.AddService(svc)
	defer set.StopService(svc)

	if !startReturns(t, set, svc, fifo) {
		t.Fatal("start blocked on a FIFO in env-dir")
	}
	found := false
	for _, e := range svc.buildEnv() {
		if e == "PLAIN=v" {
			found = true
		}
	}
	if !found {
		t.Error("PLAIN=v missing from the environment")
	}
}

// The same for an env-file that is a FIFO.
func TestEnvFileFIFODoesNotBlockStart(t *testing.T) {
	set, _ := newTestSet()
	fifo := filepath.Join(t.TempDir(), "env")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewProcessService(set, "envfile-fifo")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.SetEnvFile(fifo)
	set.AddService(svc)
	defer set.StopService(svc)

	if !startReturns(t, set, svc, fifo) {
		t.Fatal("start blocked on a FIFO env-file")
	}
}
