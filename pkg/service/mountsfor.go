package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/fstab"
)

// checkMountsFor is the mounts-for check: every mount point
// /etc/fstab names on the way to path (path itself, or a directory
// above it; "/" and swap left out) must be mounted now. systemd's
// RequiresMountsFor= pulls those mounts in and orders the unit after
// them; slinit mounts nothing for a service, so its boot graph must
// have mounted them first, and the check says so when it has not
// rather than let the service write into the directory underneath. A
// noauto entry counts too, as systemd mounts it on demand: here it
// must have been mounted by hand or by a service. With no fstab the
// check holds.
func checkMountsFor(path, fstabPath string, isMount func(string) (bool, string)) (bool, string) {
	if !filepath.IsAbs(path) {
		return false, fmt.Sprintf("mounts-for %q: not an absolute path", path)
	}
	path = filepath.Clean(path)
	entries, err := fstab.ReadFile(fstabPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, ""
		}
		return false, fmt.Sprintf("mounts-for %s: %v", path, err)
	}
	for _, e := range entries {
		mp := filepath.Clean(e.File)
		if e.VFSType == "swap" || mp == "/" || !filepath.IsAbs(mp) {
			continue
		}
		if path != mp && !strings.HasPrefix(path, mp+"/") {
			continue
		}
		if ok, why := isMount(mp); !ok {
			if why == "" {
				why = "not mounted"
			}
			return false, fmt.Sprintf("%s, which %s is on, is in /etc/fstab and %s", mp, path, why)
		}
	}
	return true, ""
}
