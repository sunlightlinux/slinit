package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestParseTimeoutFailureMode covers the three accepted spellings +
// the default-empty case + the reject path. Keeps the enum contract
// stable if someone rewires the switch.
func TestParseTimeoutFailureMode(t *testing.T) {
	cases := []struct {
		in      string
		want    TimeoutFailureMode
		wantErr bool
	}{
		{"", TimeoutFailureTerminate, false},
		{"terminate", TimeoutFailureTerminate, false},
		{"abort", TimeoutFailureAbort, false},
		{"kill", TimeoutFailureKill, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseTimeoutFailureMode(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ParseTimeoutFailureMode(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Fatalf("ParseTimeoutFailureMode(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseRestartMode(t *testing.T) {
	cases := []struct {
		in      string
		want    RestartMode
		wantErr bool
	}{
		{"", RestartModeNormal, false},
		{"normal", RestartModeNormal, false},
		{"direct", RestartModeDirect, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseRestartMode(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ParseRestartMode(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Fatalf("ParseRestartMode(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseExitType(t *testing.T) {
	cases := []struct {
		in      string
		want    ExitType
		wantErr bool
	}{
		{"", ExitTypeMain, false},
		{"main", ExitTypeMain, false},
		{"cgroup", ExitTypeCgroup, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseExitType(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ParseExitType(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Fatalf("ParseExitType(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

// TestIsForceRestartExit: only Exited() statuses whose code is in the
// configured list return true. Signals never match (documented
// contract), and an empty list disables the check entirely.
func TestIsForceRestartExit(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	rec := svc.Record()

	// Empty list — always false.
	if rec.IsForceRestartExit(makeExited(3)) {
		t.Fatalf("empty list should never match")
	}

	rec.SetRestartForceExitCodes([]int{3, 4, 7})
	if !rec.IsForceRestartExit(makeExited(3)) {
		t.Fatalf("code 3 should match")
	}
	if !rec.IsForceRestartExit(makeExited(7)) {
		t.Fatalf("code 7 should match")
	}
	if rec.IsForceRestartExit(makeExited(5)) {
		t.Fatalf("code 5 should not match")
	}
	// Signal-terminated: not eligible.
	if rec.IsForceRestartExit(makeSignaled(9)) {
		t.Fatalf("signalled exit should never match force-restart list")
	}
}

// TestCheckExecCondition: exit 0 succeeds, non-zero and empty command
// fail, timeout fails without hanging. Keeps the shell-wrap contract
// under review.
func TestCheckExecCondition(t *testing.T) {
	if ok, reason := checkExecCondition("true"); !ok {
		t.Fatalf("exit 0 should pass, got %q", reason)
	}
	if ok, _ := checkExecCondition("false"); ok {
		t.Fatalf("exit 1 should fail")
	}
	if ok, _ := checkExecCondition(""); ok {
		t.Fatalf("empty command should fail")
	}
	if ok, _ := checkExecCondition("exit 42"); ok {
		t.Fatalf("exit 42 should fail")
	}
}

// TestReadCgroupPopulated: parses the two-line cgroup.events shape and
// returns the "populated" bit.
func TestReadCgroupPopulated(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.events"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("populated 0\nfrozen 0\n")
	if pop, err := readCgroupPopulated(dir); err != nil || pop {
		t.Fatalf("populated 0 → false, got pop=%v err=%v", pop, err)
	}

	write("populated 1\nfrozen 0\n")
	if pop, err := readCgroupPopulated(dir); err != nil || !pop {
		t.Fatalf("populated 1 → true, got pop=%v err=%v", pop, err)
	}

	// Missing file: err surfaces, pop is false. Watcher treats this
	// as drain complete.
	os.Remove(filepath.Join(dir, "cgroup.events"))
	if pop, err := readCgroupPopulated(dir); err == nil || pop {
		t.Fatalf("missing file → error+false, got pop=%v err=%v", pop, err)
	}

	// Malformed body: no "populated" line → returns false, no error.
	write("frozen 0\n")
	if pop, err := readCgroupPopulated(dir); err != nil || pop {
		t.Fatalf("no populated key → false, got pop=%v err=%v", pop, err)
	}
}

// TestExecConditionTimeout verifies the sh-wrapped command is killed
// when it overruns execConditionTimeout. Bounds the test with a
// generous wall-clock budget so a slow CI doesn't spuriously fail.
func TestExecConditionTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in -short mode")
	}
	if execConditionTimeout > 12*time.Second {
		t.Skipf("execConditionTimeout=%s too long for this test", execConditionTimeout)
	}
	start := time.Now()
	ok, reason := checkExecCondition("sleep 60")
	elapsed := time.Since(start)
	if ok {
		t.Fatalf("sleep 60 should not succeed")
	}
	if elapsed > execConditionTimeout+5*time.Second {
		t.Fatalf("timeout not enforced: %s (reason %q)", elapsed, reason)
	}
}
