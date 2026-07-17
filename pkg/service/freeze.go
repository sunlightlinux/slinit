package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Freeze suspends the service by writing "1" to cgroup.freeze in the
// service's cgroup v2 directory. Semantically equivalent to
// `slinitctl pause` (SIGSTOP), but atomic across the whole cgroup
// tree (no race window while a fork/exec is happening) and cannot
// be bypassed by unshare(2). Returns an error if the service has
// no effective cgroup path, if cgroup v2 isn't in use, or if the
// write fails (typically because the kernel is not v2 or the path
// vanished).
//
// Idempotent: writing "1" to an already-frozen cgroup is a no-op.
func (sr *ServiceRecord) Freeze() error {
	return sr.writeFreezerState("1")
}

// Thaw resumes a frozen service by writing "0" to cgroup.freeze.
// The complement of Freeze; safe to call on a service that isn't
// currently frozen (no-op).
func (sr *ServiceRecord) Thaw() error {
	return sr.writeFreezerState("0")
}

// IsFrozen reads cgroup.freeze and reports whether the service is
// currently frozen. Returns false + error if the file is unreadable
// (typically means no cgroup path, or cgroup v1 in use).
func (sr *ServiceRecord) IsFrozen() (bool, error) {
	path := sr.freezerFile()
	if path == "" {
		return false, fmt.Errorf("service '%s' has no cgroup v2 path", sr.serviceName)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(data)) == "1", nil
}

func (sr *ServiceRecord) freezerFile() string {
	base := sr.EffectiveCgroupPath()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "cgroup.freeze")
}

func (sr *ServiceRecord) writeFreezerState(v string) error {
	path := sr.freezerFile()
	if path == "" {
		return fmt.Errorf("service '%s' has no cgroup v2 path", sr.serviceName)
	}
	if err := os.WriteFile(path, []byte(v), 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
