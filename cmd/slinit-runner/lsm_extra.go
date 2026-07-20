package main

import (
	"fmt"
	"os"
)

// selinuxSecurityDir is the sysfs indicator that SELinux is the
// active LSM. Overrideable in tests. Real path is populated only
// when the kernel booted with SELinux enforcing or permissive.
var selinuxSecurityDir = "/sys/fs/selinux"

// smackSecurityDir is the sysfs indicator that SMACK is the active
// LSM. Real path exists only when smackfs is mounted.
var smackSecurityDir = "/sys/fs/smackfs"

// selinuxChangeOnExec mirrors changeOnExec for SELinux: writes the
// target context to /proc/self/attr/exec so the kernel applies the
// domain transition on the next execve. Fails closed if selinuxfs
// isn't mounted — writing to attr/exec on a non-SELinux system
// silently succeeds (matches libselinux setexeccon's behaviour) and
// would leave the operator with an unconfined service, exactly what
// they were trying to prevent.
func selinuxChangeOnExec(context string) error {
	if _, err := os.Stat(selinuxSecurityDir); err != nil {
		return fmt.Errorf(
			"SELinux LSM not active (no %s); "+
				"cannot switch to context %q without the domain being enforced",
			selinuxSecurityDir, context)
	}
	f, err := os.OpenFile("/proc/self/attr/exec", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open /proc/self/attr/exec: %w", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte(context)); err != nil {
		return fmt.Errorf("write attr/exec: %w", err)
	}
	return nil
}

// smackSetProcessLabel writes the SMACK label to /proc/self/attr/
// current — SMACK changes the calling task's label immediately (no
// exec transition), and the label survives execve. Fails closed if
// smackfs isn't mounted, same reasoning as the AppArmor + SELinux
// paths: a silently-succeeded write to attr/current on a non-SMACK
// system leaves the service running with the previous label.
func smackSetProcessLabel(label string) error {
	if _, err := os.Stat(smackSecurityDir); err != nil {
		return fmt.Errorf(
			"SMACK LSM not active (no %s); "+
				"cannot set label %q without the confinement being enforced",
			smackSecurityDir, label)
	}
	f, err := os.OpenFile("/proc/self/attr/current", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open /proc/self/attr/current: %w", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte(label)); err != nil {
		return fmt.Errorf("write attr/current: %w", err)
	}
	return nil
}
