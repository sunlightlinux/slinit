package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `logfile` and `output-logger` each used to claim log-type only while
// it was unset, so whichever came FIRST in the file won and the other
// was parsed, stored, and then silently never used. Both destinations
// must now receive the service's output.
func TestLogfileAndOutputLoggerBothReceiveOutput(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "svc.log")
	seen := filepath.Join(dir, "logger-saw")
	loggerSh := filepath.Join(dir, "logger.sh")
	writeExec(t, loggerSh, "#!/bin/sh\nexec cat >> "+seen+"\n")

	set, _ := newTestSet()
	svc := NewProcessService(set, "both")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"i=0; while [ $i -lt 20 ]; do i=$((i+1)); echo \"hello-$i\"; sleep 0.05; done; sleep 60"})
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails(logFile, 0o600, -1, -1)
	svc.SetOutputLogger([]string{loggerSh})
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED", got)
	}
	time.Sleep(2 * time.Second)

	fileBody := readFileOrEmpty(t, logFile)
	loggerBody := readFileOrEmpty(t, seen)

	if !strings.Contains(fileBody, "hello-1") {
		t.Errorf("logfile did not receive the output; got %q", truncate(fileBody))
	}
	if !strings.Contains(loggerBody, "hello-1") {
		t.Errorf("output-logger did not receive the output; got %q — one "+
			"destination is still winning over the other", truncate(loggerBody))
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// With both destinations live, the logger dying must still not reach the
// service — and here it cannot even in principle, because slinit, not
// the service, is the one writing to it.
func TestLoggerDeathDoesNotDisturbTheLogfile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "svc.log")
	loggerSh := filepath.Join(dir, "logger.sh")
	writeExec(t, loggerSh, "#!/bin/sh\nsleep 0.5\nexit 1\n")

	set, _ := newTestSet()
	svc := NewProcessService(set, "resilient")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"while :; do echo tick; sleep 0.05; done"})
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails(logFile, 0o600, -1, -1)
	svc.SetOutputLogger([]string{loggerSh})
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED", got)
	}
	time.Sleep(2500 * time.Millisecond)

	if st := svc.State(); st != StateStarted {
		t.Errorf("service state = %v, want STARTED", st)
	}
	before := len(readFileOrEmpty(t, logFile))
	time.Sleep(700 * time.Millisecond)
	if after := len(readFileOrEmpty(t, logFile)); after <= before {
		t.Errorf("logfile stopped growing (%d -> %d bytes) after the logger "+
			"died — the file destination must be independent of it", before, after)
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
