package main

import (
	"os"
	"path/filepath"
	"testing"
)

// narrowBoundingSet dropped capabilities 0..40 whatever the kernel had, so
// one a newer kernel adds would stay in the bounding set of a service
// whose capability-bounding-set leaves it out.
func TestLastCapFollowsKernel(t *testing.T) {
	saved := capLastCapFile
	defer func() { capLastCapFile = saved }()

	capLastCapFile = filepath.Join(t.TempDir(), "cap_last_cap")
	if err := os.WriteFile(capLastCapFile, []byte("45\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lastCap(); got != 45 {
		t.Errorf("lastCap() = %d with cap_last_cap 45; want 45", got)
	}
	capLastCapFile = filepath.Join(t.TempDir(), "missing")
	if got := lastCap(); got != 40 {
		t.Errorf("lastCap() = %d without cap_last_cap; want the fallback 40", got)
	}
}

// The runner reads cap_last_cap before it enters a chroot: read after,
// a jail the service can write decides it, and a planted 0 left the
// bounding set unnarrowed.
func TestLastCapPrimedBeforeChroot(t *testing.T) {
	saved, savedVal, savedSet := capLastCapFile, lastCapPrimed, lastCapIsPrimed
	defer func() { capLastCapFile, lastCapPrimed, lastCapIsPrimed = saved, savedVal, savedSet }()
	capLastCapFile = filepath.Join(t.TempDir(), "cap_last_cap")
	if err := os.WriteFile(capLastCapFile, []byte("41\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	primeLastCap()
	// What a jail would plant after the chroot.
	if err := os.WriteFile(capLastCapFile, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lastCap(); got != 41 {
		t.Errorf("lastCap = %d after a later change, want the primed 41", got)
	}
}
