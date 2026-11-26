package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseSignal(t *testing.T) {
	cases := []struct {
		in   string
		want syscall.Signal
		ok   bool
	}{
		{"TERM", syscall.SIGTERM, true},
		{"SIGTERM", syscall.SIGTERM, true},
		{"sigterm", syscall.SIGTERM, true},
		{"HUP", syscall.SIGHUP, true},
		{"9", syscall.SIGKILL, true},
		{"15", syscall.SIGTERM, true},
		{"", 0, false},
		{"NOPE", 0, false},
		{"0", 0, false},
		{"99", 0, false},
	}
	for _, tc := range cases {
		got, err := ParseSignal(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("ParseSignal(%q) err=%v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("ParseSignal(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseRetryInteger(t *testing.T) {
	steps, err := ParseRetry("5", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Signal != syscall.SIGTERM || steps[0].Timeout != 5*time.Second {
		t.Errorf("step 0 = %v", steps[0])
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 5*time.Second {
		t.Errorf("step 1 = %v", steps[1])
	}
}

func TestParseRetrySchedule(t *testing.T) {
	steps, err := ParseRetry("TERM/30/KILL/5", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Signal != syscall.SIGTERM || steps[0].Timeout != 30*time.Second {
		t.Errorf("step 0 = %v", steps[0])
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 5*time.Second {
		t.Errorf("step 1 = %v", steps[1])
	}
}

func TestParseRetryTrailingSignal(t *testing.T) {
	// Trailing signal without timeout: sent, then the schedule ends
	// (sendOnly), not "wait forever".
	steps, err := ParseRetry("HUP/2/KILL", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != sendOnly {
		t.Errorf("step 1 = %v (want send-only KILL)", steps[1])
	}
}

func TestParseRetryErrors(t *testing.T) {
	bad := []string{
		"",
		"5/TERM",                        // timeout without preceding signal
		"TERM//KILL",                    // empty token
		"BOGUS/30",                      // unknown signal
		"TERM/--5",                      // "-5" is no signal
		"TERM/-",                        // dash with no signal
		"TERM/5/forever/KILL/5/forever", // forever twice
		"TERM/forever",                  // forever after a bare signal: busy loop
		"forever",                       // nothing to repeat
		"TERM/5/forever/KILL",           // repeated tail never waits
		"TERM/5/forever/KILL/0",         // ... nor with a zero timeout
	}
	for _, s := range bad {
		if _, err := ParseRetry(s, syscall.SIGTERM); err == nil {
			t.Errorf("ParseRetry(%q) should have failed", s)
		}
	}
}

// TestStopWithoutRetry: --stop with no --retry built a single step with
// Timeout 0, which waitExitProgress reads as "wait forever", so the
// `--stop --signal HUP` reload idiom never returned. OpenRC's schedule
// without --retry is the signal then 5 s, failing if the process is
// still there; with --oknodo (or --test) it sends and returns.
func TestStopWithoutRetry(t *testing.T) {
	steps, err := buildSchedule(Options{Signal: syscall.SIGHUP})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Signal != syscall.SIGHUP || steps[0].Timeout != 5*time.Second {
		t.Errorf("schedule = %+v, want [{HUP 5s}]", steps)
	}

	// A process that survives SIGHUP, as a reloading daemon does.
	cmd := exec.Command("sh", "-c", `trap "" HUP; exec sleep 30`)
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	time.Sleep(100 * time.Millisecond) // let the trap be set

	opts := Options{Signal: syscall.SIGHUP, OKnodo: true}
	steps, err = buildSchedule(opts)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runSchedule(cmd.Process.Pid, steps, opts) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("--oknodo --signal HUP: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("--stop --signal HUP --oknodo still waiting for the process to exit")
	}
}

// Debian and OpenRC document "-SIG"/"-N" signal items and "forever";
// ParseRetry rejected both.
func TestParseRetryDebianForms(t *testing.T) {
	cases := []struct {
		spec string
		want []Step
	}{
		{"-TERM/5/-KILL/5", []Step{
			{Signal: syscall.SIGTERM, Timeout: 5 * time.Second},
			{Signal: syscall.SIGKILL, Timeout: 5 * time.Second},
		}},
		{"-15/5/-9/5", []Step{
			{Signal: syscall.SIGTERM, Timeout: 5 * time.Second},
			{Signal: syscall.SIGKILL, Timeout: 5 * time.Second},
		}},
		{"TERM/30/forever", []Step{
			{Signal: syscall.SIGTERM, Timeout: 30 * time.Second},
			{Repeat: true},
			{Timeout: waitForever},
		}},
		{"TERM/5/forever/KILL/1", []Step{
			{Signal: syscall.SIGTERM, Timeout: 5 * time.Second},
			{Repeat: true},
			{Signal: syscall.SIGKILL, Timeout: 1 * time.Second},
		}},
		{"HUP/TERM/5", []Step{
			{Signal: syscall.SIGHUP, Timeout: sendOnly},
			{Signal: syscall.SIGTERM, Timeout: 5 * time.Second},
		}},
		{"TERM/0/KILL/5", []Step{
			{Signal: syscall.SIGTERM, Timeout: 0},
			{Signal: syscall.SIGKILL, Timeout: 5 * time.Second},
		}},
	}
	for _, tc := range cases {
		got, err := ParseRetry(tc.spec, syscall.SIGTERM)
		if err != nil {
			t.Errorf("ParseRetry(%q): %v", tc.spec, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseRetry(%q) = %+v, want %+v", tc.spec, got, tc.want)
		}
	}
}

// startHUPIgnorer starts a process that survives SIGHUP and appends a
// line to counter on each one. It is reaped as soon as it dies, as
// init reaps a real daemon, so kill(pid, 0) stops finding it.
func startHUPIgnorer(t *testing.T) (*exec.Cmd, string) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "hups")
	cmd := exec.Command("sh", "-c", `trap 'echo h >> "$1"' HUP; while :; do sleep 0.05; done`, "sh", counter)
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	reaped := make(chan struct{})
	go func() { cmd.Wait(); close(reaped) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-reaped })
	time.Sleep(150 * time.Millisecond) // let the trap be set
	return cmd, counter
}

// A trailing signal with no timeout used to wait forever, silently;
// as in Debian the schedule now ends there and the stop fails.
func TestRunScheduleTrailingSignalEnds(t *testing.T) {
	cmd, _ := startHUPIgnorer(t)
	steps, err := ParseRetry("HUP/1/HUP", syscall.SIGTERM)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runSchedule(cmd.Process.Pid, steps, Options{}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("schedule ended with the process alive but reported success")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("HUP/1/HUP still waiting after the last signal")
	}
}

// forever repeats the tail: HUP is sent again every second until the
// process goes.
func TestRunScheduleForeverRepeats(t *testing.T) {
	cmd, counter := startHUPIgnorer(t)
	steps, err := ParseRetry("HUP/1/forever/HUP/1", syscall.SIGTERM)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runSchedule(cmd.Process.Pid, steps, Options{}) }()
	time.Sleep(3500 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("forever schedule returned early: %v", err)
	default:
	}
	data, _ := os.ReadFile(counter)
	if n := strings.Count(string(data), "h"); n < 3 {
		t.Errorf("%d HUPs in 3.5 s, want at least 3 (one plus repeats)", n)
	}
	cmd.Process.Kill()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("process gone, schedule says %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forever schedule did not notice the process exit")
	}
}
