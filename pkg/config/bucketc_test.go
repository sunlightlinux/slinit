package config

import (
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseBucketCDirectives verifies each v261/262 catch-up directive
// round-trips through the parser. Runtime behaviour lives in the
// state-machine tests and the ceres install smoke suite.
func TestParseBucketCDirectives(t *testing.T) {
	input := `
type = process
command = /bin/true
cpuset-partition = root
cache-directory-quota = 100M
logs-directory-quota = 50M
state-directory-quota = 500M
cache-directory-accounting = yes
logs-directory-accounting = yes
state-directory-accounting = yes
startup-allowed-cpus = 0-3
startup-allowed-memory-nodes = 0
timeout-stop-failure-mode = abort
watchdog-signal = SIGUSR1
final-kill-signal = SIGKILL
survive-final-kill-signal = yes
restart-kill-signal = SIGHUP
kill-mode = mixed
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.CpusetPartition != "root" {
		t.Errorf("cpuset-partition = %q, want root", desc.CpusetPartition)
	}
	if desc.CacheDirectoryQuota != 100*1024*1024 {
		t.Errorf("cache-directory-quota = %d, want %d", desc.CacheDirectoryQuota, 100*1024*1024)
	}
	if desc.LogsDirectoryQuota != 50*1024*1024 {
		t.Errorf("logs-directory-quota = %d, want %d", desc.LogsDirectoryQuota, 50*1024*1024)
	}
	if !desc.CacheDirectoryAccounting || !desc.LogsDirectoryAccounting || !desc.StateDirectoryAccounting {
		t.Errorf("accounting flags: %v %v %v",
			desc.CacheDirectoryAccounting, desc.LogsDirectoryAccounting, desc.StateDirectoryAccounting)
	}
	if desc.StartupAllowedCPUs != "0-3" {
		t.Errorf("startup-allowed-cpus = %q", desc.StartupAllowedCPUs)
	}
	if desc.StartupAllowedMemoryNodes != "0" {
		t.Errorf("startup-allowed-memory-nodes = %q", desc.StartupAllowedMemoryNodes)
	}
	if desc.TimeoutStopFailureMode != service.TimeoutFailureAbort {
		t.Errorf("timeout-stop-failure-mode = %v, want abort", desc.TimeoutStopFailureMode)
	}
	if desc.WatchdogSignal != syscall.SIGUSR1 {
		t.Errorf("watchdog-signal = %v, want SIGUSR1", desc.WatchdogSignal)
	}
	if desc.FinalKillSignal != syscall.SIGKILL {
		t.Errorf("final-kill-signal = %v, want SIGKILL", desc.FinalKillSignal)
	}
	if !desc.SurviveFinalKillSignal {
		t.Errorf("survive-final-kill-signal should be true")
	}
	if desc.RestartKillSignal != syscall.SIGHUP {
		t.Errorf("restart-kill-signal = %v, want SIGHUP", desc.RestartKillSignal)
	}
	if desc.KillMode != service.KillModeMixed {
		t.Errorf("kill-mode = %v, want mixed", desc.KillMode)
	}
}

func TestParseBucketCRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"kill-mode bogus", "type = process\ncommand = /bin/true\nkill-mode = bogus\n"},
		{"cpuset-partition bogus", "type = process\ncommand = /bin/true\ncpuset-partition = bogus\n"},
		{"timeout-stop-failure-mode bogus",
			"type = process\ncommand = /bin/true\ntimeout-stop-failure-mode = bogus\n"},
	}
	for _, tc := range cases {
		if _, err := Parse(strings.NewReader(tc.body), "svc", "test-file"); err == nil {
			t.Errorf("%s: expected parse error", tc.name)
		}
	}
}
