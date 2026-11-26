package service

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func mkfifo(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fifoStart starts a sleep service configured by setup and fails the
// test if the start blocks; it returns the state the start reached.
func fifoStart(t *testing.T, setup func(*ProcessService, string)) ServiceState {
	t.Helper()
	set, _ := newTestSet()
	f := mkfifo(t)
	svc := NewProcessService(set, "fifo-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	setup(svc, f)
	set.AddService(svc)
	defer set.StopService(svc)
	if !startReturns(t, set, svc, f) {
		t.Fatal("start blocked on a FIFO")
	}
	time.Sleep(50 * time.Millisecond)
	return svc.State()
}

// A log-file that is a FIFO with no reader blocked open(O_WRONLY) in
// the start, under the service set's lock, until a reader appeared.
// It now fails the start.
func TestLogFileFIFONoReaderDoesNotBlockStart(t *testing.T) {
	st := fifoStart(t, func(s *ProcessService, f string) {
		s.SetLogType(LogToFile)
		s.SetLogFileDetails(f, 0o600, -1, -1)
	})
	if st == StateStarted {
		t.Errorf("state %v, want the start to fail", st)
	}
}

// A FIFO log-file whose reader is already there still works.
func TestLogFileFIFOWithReader(t *testing.T) {
	set, _ := newTestSet()
	f := mkfifo(t)
	r, err := os.OpenFile(f, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	svc := NewProcessService(set, "fifo-log")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo hello; exec sleep 60"})
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails(f, 0o600, -1, -1)
	set.AddService(svc)
	defer set.StopService(svc)
	if !startReturns(t, set, svc, f) {
		t.Fatal("start blocked")
	}
	r.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, err := r.Read(buf)
	if string(buf[:n]) != "hello\n" {
		t.Errorf("read %q (%v), want hello", buf[:n], err)
	}
}

// required-files checks a file is readable by opening it; a FIFO with
// no writer blocked that open, under the service set's lock. A FIFO
// exists and is readable, so the start goes ahead.
func TestRequiredFileFIFODoesNotBlockStart(t *testing.T) {
	st := fifoStart(t, func(s *ProcessService, f string) {
		s.Record().SetRequiredPaths([]string{f}, nil)
	})
	if st != StateStarted {
		t.Errorf("state %v, want STARTED", st)
	}
}

// standard-input-file was opened in the start, under the service
// set's lock; a FIFO with no writer blocked that open. The service now
// starts with the FIFO as fd 0.
func TestStdinFileFIFODoesNotBlockStart(t *testing.T) {
	st := fifoStart(t, func(s *ProcessService, f string) {
		s.Record().SetStandardInputFile(f)
	})
	if st != StateStarted {
		t.Errorf("state %v, want STARTED", st)
	}
}

// lock-file was opened for writing in the start, under the service
// set's lock; a FIFO at the path with no reader blocked that open. Now
// the start fails, as for any lock-file that cannot be opened.
func TestLockFileFIFODoesNotBlockStart(t *testing.T) {
	st := fifoStart(t, func(s *ProcessService, f string) {
		s.SetLockFile(f)
	})
	if st == StateStarted {
		t.Errorf("state %v, want the start to fail", st)
	}
}
