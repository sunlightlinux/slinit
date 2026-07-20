package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSELinuxFailsClosedWithoutLSM mirrors the AppArmor fail-closed
// test: without /sys/fs/selinux, the runner must abort BEFORE
// writing to /proc/self/attr/exec — a silent write on a non-SELinux
// system leaves the service unconfined.
func TestSELinuxFailsClosedWithoutLSM(t *testing.T) {
	orig := selinuxSecurityDir
	selinuxSecurityDir = filepath.Join(t.TempDir(), "not-selinux")
	defer func() { selinuxSecurityDir = orig }()

	err := selinuxChangeOnExec("system_u:system_r:my_service_t:s0")
	if err == nil {
		t.Fatal("selinuxChangeOnExec returned nil when SELinux LSM is absent — should fail closed")
	}
	if !strings.Contains(err.Error(), "SELinux LSM not active") {
		t.Errorf("error must mention SELinux LSM state; got: %v", err)
	}
	if !strings.Contains(err.Error(), "my_service_t") {
		t.Errorf("error must name the context; got: %v", err)
	}
}

// TestSMACKFailsClosedWithoutLSM: same shape as SELinux, but for
// /sys/fs/smackfs. SMACK's attr/current write also succeeds silently
// on non-SMACK systems, so the sysfs probe is what guards intent.
func TestSMACKFailsClosedWithoutLSM(t *testing.T) {
	orig := smackSecurityDir
	smackSecurityDir = filepath.Join(t.TempDir(), "not-smackfs")
	defer func() { smackSecurityDir = orig }()

	err := smackSetProcessLabel("MyServiceLabel")
	if err == nil {
		t.Fatal("smackSetProcessLabel returned nil when SMACK LSM is absent — should fail closed")
	}
	if !strings.Contains(err.Error(), "SMACK LSM not active") {
		t.Errorf("error must mention SMACK LSM state; got: %v", err)
	}
	if !strings.Contains(err.Error(), "MyServiceLabel") {
		t.Errorf("error must name the label; got: %v", err)
	}
}
