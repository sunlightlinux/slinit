// slinit-mountinfo — OpenRC-compatible /proc/mounts query utility.
//
// Drop-in replacement for OpenRC's mountinfo(8): parses the kernel
// mount table, applies regex + netdev + positional filters, and
// prints one field per matching entry in reverse order (the order
// init.d scripts want for umount sequencing).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/fstab"
	"github.com/sunlightlinux/slinit/pkg/mounts"
)

const (
	exitOK       = 0
	exitFailure  = 1
	exitBadUsage = 2
)

// outputField selects which field of the entry we print for matching
// rows. Default is the mountpoint (fs_file).
type outputField int

const (
	fieldMountPoint outputField = iota
	fieldOptions
	fieldFstype
	fieldNode // fs_spec / mnt_fsname
)

type netFilter int

const (
	netIgnore netFilter = iota
	netYes
	netNo
)

type options struct {
	// Output selector.
	field outputField

	// Regex filters (nil = don't filter on that dimension).
	fstypeRe, skipFstypeRe   *regexp.Regexp
	nodeRe, skipNodeRe       *regexp.Regexp
	optionsRe, skipOptionsRe *regexp.Regexp
	pointRe, skipPointRe     *regexp.Regexp

	// _netdev via /etc/fstab.
	netdev netFilter

	// Positional list of mountpoints. When non-empty, only entries
	// whose fs_file matches one of these are printed.
	mountpoints []string

	// Test seams — non-standard.
	procMounts string
	etcFstab   string
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		switch err {
		case errHelp:
			os.Exit(exitOK)
		case errVersion:
			fmt.Printf("slinit-mountinfo %s\n", version)
			os.Exit(exitOK)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitBadUsage)
	}
	os.Exit(run(opts))
}

// run reads the mount table and drives the filter chain. Returns an
// LSB-style exit code so init.d scripts can `if slinit-mountinfo…`
// their conditionals.
func run(opts options) int {
	entries, err := fstab.ReadFile(opts.procMounts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", opts.procMounts, err)
		return exitFailure
	}

	// fstab is only consulted when a netdev filter is active. Missing
	// fstab is fatal only in that case — the rest of the tool works
	// without one.
	var fstabEntries []fstab.Entry
	if opts.netdev != netIgnore {
		fstabEntries, err = fstab.ReadFile(opts.etcFstab)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", opts.etcFstab, err)
			return exitFailure
		}
	}

	quiet := isTruthy(os.Getenv("EINFO_QUIET"))
	matched := 0
	// Reverse iteration matches OpenRC: init.d scripts want the
	// deepest / most-recent mount first for umount sequencing.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if !accept(e, opts, fstabEntries) {
			continue
		}
		matched++
		if quiet {
			continue
		}
		printField(e, opts.field)
	}
	if matched == 0 {
		return exitFailure
	}
	return exitOK
}

// accept walks every filter in the same order as OpenRC's
// process_mount(), so a straight port of a script that relied on
// filter precedence keeps its shape.
func accept(e fstab.Entry, opts options, fstabEntries []fstab.Entry) bool {
	// Silly rootfs — skip on Linux, matches OpenRC.
	if e.VFSType == "rootfs" {
		return false
	}

	// Netdev filter short-circuits the other regexen (matches
	// OpenRC's if/else structure).
	if opts.netdev != netIgnore {
		status := mounts.LookupNetdev(fstabEntries, e.File)
		if status == mounts.NetdevUnknown {
			return false
		}
		if opts.netdev == netYes && status != mounts.NetdevYes {
			return false
		}
		if opts.netdev == netNo && status != mounts.NetdevNo {
			return false
		}
	} else {
		if opts.nodeRe != nil && !opts.nodeRe.MatchString(e.Spec) {
			return false
		}
		if opts.skipNodeRe != nil && opts.skipNodeRe.MatchString(e.Spec) {
			return false
		}
		if opts.fstypeRe != nil && !opts.fstypeRe.MatchString(e.VFSType) {
			return false
		}
		if opts.skipFstypeRe != nil && opts.skipFstypeRe.MatchString(e.VFSType) {
			return false
		}
		if opts.optionsRe != nil && !opts.optionsRe.MatchString(e.MntOps) {
			return false
		}
		if opts.skipOptionsRe != nil && opts.skipOptionsRe.MatchString(e.MntOps) {
			return false
		}
	}

	if opts.pointRe != nil && !opts.pointRe.MatchString(e.File) {
		return false
	}
	if opts.skipPointRe != nil && opts.skipPointRe.MatchString(e.File) {
		return false
	}

	if len(opts.mountpoints) > 0 {
		found := false
		for _, mp := range opts.mountpoints {
			if e.File == mp {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func printField(e fstab.Entry, f outputField) {
	switch f {
	case fieldMountPoint:
		fmt.Println(e.File)
	case fieldOptions:
		fmt.Println(e.MntOps)
	case fieldFstype:
		fmt.Println(e.VFSType)
	case fieldNode:
		fmt.Println(e.Spec)
	}
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "y", "yes", "true", "on":
		return true
	}
	return false
}

// realpath is used on positional mountpoint arguments so that a
// symlinked pathspec still matches its canonical form in the mount
// table. Errors fall back to the original path — OpenRC does the
// same via realpath(3).
func realpath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return real
}
