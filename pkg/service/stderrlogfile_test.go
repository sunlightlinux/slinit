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
