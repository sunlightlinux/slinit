package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A logger command that exits used to take its service down with it:
// the parent had closed the pipe's read end after the fork, so the
// service's next write hit a reader-less pipe and the kernel killed it
// with SIGPIPE. Measured before the fix, with a logger that exits after
// one second: "process killed by signal broken pipe" and, under
// restart = yes, four kills and four restarts in eight seconds.
//
// The service must now outlive its logger, and the logger must come
// back.
func TestServiceSurvivesItsOutputLoggerExiting(t *testing.T) {
	dir := t.TempDir()
	// A logger that records each start and then exits, so we can count
	// how many times it was restarted.
	marks := filepath.Join(dir, "starts")
	loggerSh := filepath.Join(dir, "logger.sh")
	writeExec(t, loggerSh, "#!/bin/sh\necho start >> "+marks+"\nexec sleep 0.4\n")

	set, _ := newTestSet()
	svc := NewProcessService(set, "talker")
	// Write continuously: without a reader the first write after the
	// logger dies is what used to kill the service.
	svc.SetCommand([]string{"/bin/sh", "-c", "while :; do echo tick; sleep 0.05; done"})
	svc.SetLogType(LogToCommand)
	svc.SetOutputLogger([]string{loggerSh})
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED", got)
	}

	// Long enough for the logger to exit twice and be restarted, with
	// the 1s restart delay in between.
	time.Sleep(3500 * time.Millisecond)

	if st := svc.State(); st != StateStarted {
		t.Errorf("service state = %v after its logger exited, want STARTED "+
			"(it used to be killed by SIGPIPE)", st)
	}
	if n := countLines(t, marks); n < 2 {
		t.Errorf("logger was started %d time(s); want at least 2 — it is not "+
			"being restarted", n)
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// Stopping the service must take the logger with it and release the
// pipe, rather than leaving a supervisor restarting a logger for a
// service that is gone.
func TestStoppingServiceStopsItsLogger(t *testing.T) {
	dir := t.TempDir()
	marks := filepath.Join(dir, "starts")
	loggerSh := filepath.Join(dir, "logger.sh")
	writeExec(t, loggerSh, "#!/bin/sh\necho start >> "+marks+"\nexec cat > /dev/null\n")

	set, _ := newTestSet()
	svc := NewProcessService(set, "quiet")
	svc.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	svc.SetLogType(LogToCommand)
	svc.SetOutputLogger([]string{loggerSh})
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED", got)
	}
	// The logger starts asynchronously; wait for its first start to be
	// recorded, or a slow first start (under -race, say) would be counted
	// below as a restart after the stop.
	deadline := time.Now().Add(5 * time.Second)
	for countLines(t, marks) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	started := countLines(t, marks)
	if started == 0 {
		t.Fatal("logger never started")
	}

	set.StopService(svc)
	if got := waitState(t, svc, StateStopped, 5*time.Second); got != StateStopped {
		t.Fatalf("service state = %v, want STOPPED", got)
	}

	// Well past the restart delay: a supervisor that kept going would
	// have started the logger again by now.
	time.Sleep(2 * time.Second)
	if n := countLines(t, marks); n != started {
		t.Errorf("logger started %d time(s) after the service stopped (was %d) "+
			"— the supervisor outlived its service", n, started)
	}
}

// A logger that cannot be restarted at all must not leave the service
// blocked on a pipe nobody drains. The supervisor gives up after a
// bounded number of attempts and discards the output instead.
func TestUnrestartableLoggerDoesNotBlockTheService(t *testing.T) {
	dir := t.TempDir()
	loggerSh := filepath.Join(dir, "logger.sh")
	// Runs once, then we remove it so every restart fails at exec.
	writeExec(t, loggerSh, "#!/bin/sh\nexec sleep 0.2\n")

	set, _ := newTestSet()
	svc := NewProcessService(set, "chatty")
	// Write far more than a pipe buffer holds, so a service that is
	// never drained is provably stuck rather than merely idle.
	svc.SetCommand([]string{"/bin/sh", "-c",
		"i=0; while :; do i=$((i+1)); echo \"line $i $(date)\"; done"})
	svc.SetLogType(LogToCommand)
	svc.SetOutputLogger([]string{loggerSh})
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED", got)
	}
	if err := os.Remove(loggerSh); err != nil {
		t.Fatalf("remove logger: %v", err)
	}

	// 5 attempts at ~1s each, plus slack.
	time.Sleep(9 * time.Second)

	if st := svc.State(); st != StateStarted {
		t.Errorf("service state = %v, want STARTED — a logger that cannot be "+
			"restarted must not take the service with it", st)
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
