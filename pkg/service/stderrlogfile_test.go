package service

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stderr used to have nowhere to go but the one logfile (merged) or an
// external command via error-logger. With stderr-logfile the two
// streams land in two files, and nothing from one appears in the other.
func TestStderrLogfileSeparatesTheStreams(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.log")
	errf := filepath.Join(dir, "err.log")

	set, _ := newTestSet()
	svc := NewProcessService(set, "twostreams")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"i=0; while [ $i -lt 10 ]; do i=$((i+1)); echo \"to-stdout-$i\"; " +
			"echo \"to-stderr-$i\" >&2; sleep 0.05; done; sleep 60"})
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails(out, 0o600, -1, -1)
	svc.SetStderrLogFile(errf)
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED", got)
	}
	time.Sleep(2 * time.Second)

	outBody := readFileOrEmpty(t, out)
	errBody := readFileOrEmpty(t, errf)

	if !strings.Contains(outBody, "to-stdout-1") {
		t.Errorf("stdout did not reach logfile; got %q", truncate(outBody))
	}
	if !strings.Contains(errBody, "to-stderr-1") {
		t.Errorf("stderr did not reach stderr-logfile; got %q", truncate(errBody))
	}
	if strings.Contains(outBody, "to-stderr-") {
		t.Errorf("stderr leaked into the stdout file: %q", truncate(outBody))
	}
	if strings.Contains(errBody, "to-stdout-") {
		t.Errorf("stdout leaked into the stderr file: %q", truncate(errBody))
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// Without the directive, stderr still merges into the single logfile —
// the behaviour every existing service relies on.
func TestWithoutStderrLogfileStreamsStillMerge(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.log")

	set, _ := newTestSet()
	svc := NewProcessService(set, "merged")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"echo on-stdout; echo on-stderr >&2; sleep 60"})
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails(out, 0o600, -1, -1)
	// Force the rotator path so this exercises the same pipeline.
	svc.SetLogRotation(1<<20, 3, 0)
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED", got)
	}
	time.Sleep(1500 * time.Millisecond)

	body := readFileOrEmpty(t, out)
	for _, want := range []string{"on-stdout", "on-stderr"} {
		if !strings.Contains(body, want) {
			t.Errorf("logfile missing %q; got %q", want, truncate(body))
		}
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// stderr-logfile on its own, as the parser accepts it: log-type becomes
// file with no logfile for stdout. The stderr rotator was started only
// next to a stdout pipe, so here nothing ever read stderr: the file was
// never written, and a service that filled the 64 KiB pipe blocked on
// its next write to stderr.
func TestStderrLogfileWithoutLogfile(t *testing.T) {
	dir := t.TempDir()
	errf := filepath.Join(dir, "err.log")
	done := filepath.Join(dir, "done")

	set, _ := newTestSet()
	svc := NewProcessService(set, "stderronly")
	// ~100 KiB to stderr, more than a pipe holds, then a marker.
	svc.SetCommand([]string{"/bin/sh", "-c",
		"i=0; while [ $i -lt 1600 ]; do i=$((i+1)); " +
			"echo \"to-stderr-$i-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\" >&2; done; " +
			"touch " + done + "; sleep 60"})
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails("", 0o600, -1, -1)
	svc.SetStderrLogFile(errf)
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !fileExists(done)() {
		time.Sleep(50 * time.Millisecond)
	}
	if !fileExists(done)() {
		t.Errorf("service blocked writing to stderr: nothing reads the pipe")
	}
	if body := readFileOrEmpty(t, errf); !strings.Contains(body, "to-stderr-1600-") {
		t.Errorf("stderr did not reach stderr-logfile; got %q", truncate(body))
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// error-logger without output-logger: the same wiring gap sent stderr to
// /dev/null instead of the logger.
func TestErrorLoggerWithoutOutputLogger(t *testing.T) {
	dir := t.TempDir()
	errf := filepath.Join(dir, "err.log")

	set, _ := newTestSet()
	svc := NewProcessService(set, "errloggeronly")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo to-stderr >&2; sleep 60"})
	svc.SetLogType(LogToCommand)
	svc.SetErrorLogger([]string{"/bin/sh", "-c", "cat > " + errf})
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(readFileOrEmpty(t, errf), "to-stderr") {
		time.Sleep(50 * time.Millisecond)
	}
	if body := readFileOrEmpty(t, errf); !strings.Contains(body, "to-stderr") {
		t.Errorf("stderr did not reach the error-logger; got %q", truncate(body))
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}
