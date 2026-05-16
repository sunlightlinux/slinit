package process

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

// TestNeedsRunnerWrapAppArmor verifies the runner wrap triggers on an
// apparmor switch (child-side aa_change_onexec) but not on apparmor load
// (which is a parent-side, kernel-wide operation).
func TestNeedsRunnerWrapAppArmor(t *testing.T) {
	if !needsRunnerWrap(ExecParams{AppArmorProfile: "svc-profile"}) {
		t.Error("apparmor switch should require the runner wrap")
	}
	if needsRunnerWrap(ExecParams{AppArmorLoadProfile: "/etc/apparmor.d/svc"}) {
		t.Error("apparmor load alone must not require the runner wrap")
	}
}

// TestWrapWithRunnerAppArmor verifies the --apparmor flag is emitted and
// ordered after the NUMA/mlockall flags but before the "--" separator.
func TestWrapWithRunnerAppArmor(t *testing.T) {
	p := ExecParams{
		Command:         []string{"/usr/bin/svc", "arg"},
		MlockallFlags:   unix.MCL_CURRENT,
		AppArmorProfile: "svc-profile",
		RunnerPath:      "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{
		"/sbin/slinit-runner",
		"--mlockall=1",
		"--apparmor=svc-profile",
		"--",
		"/usr/bin/svc",
		"arg",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}

// TestWrapWithRunnerAppArmorOnly verifies a profile switch with no other
// runner-requiring option still produces a correct argv.
func TestWrapWithRunnerAppArmorOnly(t *testing.T) {
	p := ExecParams{
		Command:         []string{"/bin/true"},
		AppArmorProfile: "p",
		RunnerPath:      "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{"/sbin/slinit-runner", "--apparmor=p", "--", "/bin/true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}
