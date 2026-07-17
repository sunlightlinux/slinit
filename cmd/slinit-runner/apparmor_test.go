package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestChangeOnExecFailsClosedWithoutLSM pins the fail-closed contract:
// when /sys/kernel/security/apparmor is absent, changeOnExec must abort
// with a clear error BEFORE touching /proc/self/attr/exec. On a kernel
// with the LSM framework compiled in but AppArmor disabled, attr/exec
// exists and accepts the write silently — the profile transition is a
// no-op and the service would run unconfined. That's exactly the state
// this test exercises via a redirected sysfs dir that we guarantee to
// be missing.
func TestChangeOnExecFailsClosedWithoutLSM(t *testing.T) {
	orig := apparmorSecurityDir
	// Point at a path that provably doesn't exist. TempDir + a joined
	// non-existent leaf ensures we never race with a real host state.
	apparmorSecurityDir = filepath.Join(t.TempDir(), "not-apparmor")
	defer func() { apparmorSecurityDir = orig }()

	err := changeOnExec("some_profile")
	if err == nil {
		t.Fatal("changeOnExec returned nil when AppArmor LSM is absent — should fail closed")
	}
	if !strings.Contains(err.Error(), "AppArmor LSM not active") {
		t.Errorf("error message must mention AppArmor LSM state; got: %v", err)
	}
	if !strings.Contains(err.Error(), "some_profile") {
		t.Errorf("error message must name the profile; got: %v", err)
	}
}
