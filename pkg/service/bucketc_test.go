package service

import (
	"syscall"
	"testing"
)

// TestKillModeZeroValueIsProcess is a load-bearing invariant: every
// ServiceRecord created without an explicit kill-mode= directive
// carries KillMode = 0, which MUST be KillModeProcess (signal only
// the main pid). If someone reorders the iota list, killsToGroup()
// will silently start walking the cgroup for every service on the
// system — the exact regression caught by acceptance test
// 59-options-kill-all-on-stop's negative-control probe on ceres
// v1.10.49.
func TestKillModeZeroValueIsProcess(t *testing.T) {
	if KillModeProcess != 0 {
		t.Fatalf("KillModeProcess must be 0 (zero-value default), got %d", KillModeProcess)
	}
	var zero KillMode
	if zero != KillModeProcess {
		t.Fatalf("zero-value KillMode must == KillModeProcess, got %v", zero)
	}
}

// TestParseKillMode covers the four named modes plus rejection of a
// typo. The state-machine consumer is killsToGroup() in process.go —
// exercised indirectly by the functional suite.
func TestParseKillMode(t *testing.T) {
	cases := []struct {
		in      string
		want    KillMode
		wantErr bool
	}{
		{"", KillModeProcess, false},
		{"process", KillModeProcess, false},
		{"control-group", KillModeControlGroup, false},
		{"mixed", KillModeMixed, false},
		{"none", KillModeNone, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseKillMode(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ParseKillMode(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Errorf("ParseKillMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestRecordAccessorsBucketC: the setters/accessors round-trip. Not
// exciting but catches typos in the field names — Go's zero-value
// defaults would otherwise let a `sr.watchdogSignal = ...` typo
// masquerade as intentional silence.
func TestRecordAccessorsBucketC(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	rec := svc.Record()

	rec.SetCpusetPartition("root")
	if rec.CpusetPartition() != "root" {
		t.Errorf("cpuset-partition round-trip failed")
	}
	rec.SetWatchdogSignal(syscall.SIGUSR2)
	if rec.WatchdogSignal() != syscall.SIGUSR2 {
		t.Errorf("watchdog-signal round-trip failed")
	}
	// Default FinalKillSignal is SIGKILL even without explicit set.
	if rec.FinalKillSignal() != syscall.SIGKILL {
		t.Errorf("default final-kill-signal must be SIGKILL, got %v", rec.FinalKillSignal())
	}
	rec.SetFinalKillSignal(syscall.SIGTERM)
	if rec.FinalKillSignal() != syscall.SIGTERM {
		t.Errorf("explicit final-kill-signal not honoured")
	}
	rec.SetSurviveFinalKillSignal(true)
	if !rec.SurviveFinalKillSignal() {
		t.Errorf("survive-final-kill-signal round-trip failed")
	}
	rec.SetKillMode(KillModeMixed)
	if rec.KillMode() != KillModeMixed {
		t.Errorf("kill-mode round-trip failed")
	}
	rec.SetTimeoutStopFailureMode(TimeoutFailureAbort)
	if rec.TimeoutStopFailureMode() != TimeoutFailureAbort {
		t.Errorf("timeout-stop-failure-mode round-trip failed")
	}
}
