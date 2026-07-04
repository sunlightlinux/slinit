// Package mounts is a thin wrapper around pkg/fstab that reads
// /proc/mounts (the kernel's view of the currently-mounted
// filesystems). Format is identical to /etc/fstab so the parser is
// reused verbatim; this package only adds the "is this path
// mounted?" and "does fstab flag this as _netdev?" helpers that
// mountinfo(8) needs.
package mounts

import (
	"strings"

	"github.com/sunlightlinux/slinit/pkg/fstab"
)

// DefaultPath is the standard procfs mount table.
var DefaultPath = "/proc/mounts"

// Read parses the current mount table. Kept as a var so tests can
// point at a fixture without touching /proc.
func Read() ([]fstab.Entry, error) {
	return fstab.ReadFile(DefaultPath)
}

// IsMounted returns true iff any entry in the current mount table
// has File == path. Useful for the pre-mount duplicate-check init.d
// scripts perform.
func IsMounted(path string) (bool, error) {
	entries, err := Read()
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.File == path {
			return true, nil
		}
	}
	return false, nil
}

// NetdevStatus reports how a mountpoint is annotated in /etc/fstab
// with respect to the "_netdev" option. This is what mountinfo(8)'s
// --netdev / --nonetdev filters key off of.
type NetdevStatus int

const (
	// NetdevUnknown means the mountpoint is not present in fstab; the
	// filter treats this as "cannot decide" and skips the entry.
	NetdevUnknown NetdevStatus = -1
	// NetdevYes means fstab flags the mountpoint as _netdev.
	NetdevYes NetdevStatus = 0
	// NetdevNo means fstab knows the mountpoint but does not flag it
	// _netdev.
	NetdevNo NetdevStatus = 1
)

// LookupNetdev checks fstabEntries (already parsed via pkg/fstab) for
// mntpath and reports whether it carries the _netdev mount option.
// The polarity matches OpenRC's process_mount() so the filter logic
// downstream is a straight port.
func LookupNetdev(fstabEntries []fstab.Entry, mntpath string) NetdevStatus {
	e := fstab.FindByFile(fstabEntries, mntpath)
	if e == nil {
		return NetdevUnknown
	}
	if strings.Contains(e.MntOps, "_netdev") {
		return NetdevYes
	}
	return NetdevNo
}
