package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

func TestSchedPolicyParsing(t *testing.T) {
	cases := []struct {
		input string
		want  uint32
	}{
		{"fifo", unix.SCHED_FIFO},
		{"FIFO", unix.SCHED_FIFO},
		{"realtime", unix.SCHED_FIFO},
		{"rr", unix.SCHED_RR},
		{"batch", unix.SCHED_BATCH},
		{"idle", unix.SCHED_IDLE},
		{"deadline", unix.SCHED_DEADLINE},
		{"other", unix.SCHED_NORMAL},
		{"normal", unix.SCHED_NORMAL},
	}
	for _, c := range cases {
		got, err := parseSchedPolicy(c.input)
		if err != nil {
			t.Errorf("parseSchedPolicy(%q): unexpected error %v", c.input, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSchedPolicy(%q) = %d, want %d", c.input, got, c.want)
		}
	}
	if _, err := parseSchedPolicy("garbage"); err == nil {
		t.Error("expected error for unknown policy")
	}
}

func TestSchedDurationParsing(t *testing.T) {
	cases := []struct {
		input string
		want  uint64
	}{
		{"1ms", 1_000_000},
		{"500us", 500_000},
		{"42ns", 42},
		{"1s", 1_000_000_000},
		{"500", 500}, // bare integer = ns
	}
	for _, c := range cases {
		got, err := parseSchedDuration(c.input)
		if err != nil {
			t.Errorf("parseSchedDuration(%q): %v", c.input, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSchedDuration(%q) = %d, want %d", c.input, got, c.want)
		}
	}
	for _, bad := range []string{"0", "0s", "-1ms", "abc"} {
		if _, err := parseSchedDuration(bad); err == nil {
			t.Errorf("parseSchedDuration(%q): expected error", bad)
		}
	}
}

func TestSchedFifoServiceLoadsWithPriority(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "rt",
		"type = process\n"+
			"command = /bin/sleep 60\n"+
			"sched-policy = fifo\n"+
			"sched-priority = 50\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	if _, err := loader.LoadService("rt"); err != nil {
		t.Fatalf("LoadService: %v", err)
	}
}

func TestSchedFifoWithoutPriorityRejected(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "rt",
		"type = process\n"+
			"command = /bin/sleep 60\n"+
			"sched-policy = fifo\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("rt")
	if err == nil {
		t.Fatal("expected load error: FIFO without priority")
	}
	if !strings.Contains(err.Error(), "priority") {
		t.Errorf("expected 'priority' in error, got: %v", err)
	}
}

func TestSchedDeadlineRequiresAllThree(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "dl",
		"type = process\n"+
			"command = /bin/sleep 60\n"+
			"sched-policy = deadline\n"+
			"sched-runtime = 1ms\n"+
			"sched-deadline = 5ms\n") // missing period

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("dl")
	if err == nil {
		t.Fatal("expected error: DEADLINE missing period")
	}
}

func TestSchedDeadlineInvariantEnforced(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "dl",
		"type = process\n"+
			"command = /bin/sleep 60\n"+
			"sched-policy = deadline\n"+
			"sched-runtime = 10ms\n"+
			"sched-deadline = 5ms\n"+ // runtime > deadline
			"sched-period = 20ms\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("dl")
	if err == nil {
		t.Fatal("expected error: runtime > deadline")
	}
	if !strings.Contains(err.Error(), "runtime") || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("expected 'runtime' and 'deadline' in error, got: %v", err)
	}
}

func TestSchedPriorityWithoutPolicyRejected(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "stray",
		"type = process\n"+
			"command = /bin/sleep 60\n"+
			"sched-priority = 50\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("stray")
	if err == nil {
		t.Fatal("expected error: priority without policy")
	}
}

func TestSchedPriorityMeaninglessForDeadline(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "mix",
		"type = process\n"+
			"command = /bin/sleep 60\n"+
			"sched-policy = deadline\n"+
			"sched-priority = 50\n"+
			"sched-runtime = 1ms\n"+
			"sched-deadline = 5ms\n"+
			"sched-period = 10ms\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("mix")
	if err == nil {
		t.Fatal("expected error: priority irrelevant for DEADLINE")
	}
}

func TestSchedPriorityRangeValidation(t *testing.T) {
	for _, bad := range []string{"0", "100", "-1", "abc"} {
		input := "type = process\ncommand = /bin/true\n" +
			"sched-policy = fifo\nsched-priority = " + bad + "\n"
		_, err := Parse(strings.NewReader(input), "rt", "test")
		if err == nil {
			t.Errorf("priority=%q should be rejected", bad)
		}
	}
}

func TestSchedResetOnForkDefault(t *testing.T) {
	desc := NewServiceDescription("test")
	if !desc.SchedResetOnFork {
		t.Error("default SchedResetOnFork should be true")
	}
}
