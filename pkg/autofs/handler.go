package autofs

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// MountHandler processes autofs kernel notifications and performs real mounts.
type MountHandler struct {
	logger *log.Logger
}

// NewMountHandler creates a handler with the given logger.
func NewMountHandler(logger *log.Logger) *MountHandler {
	return &MountHandler{logger: logger}
}

// HandleMissing is called when a path lookup occurs under an autofs mount point.
// It performs the real mount and notifies the kernel.
func (mh *MountHandler) HandleMissing(am *AutofsMount, pkt *V5Packet) {
	name := pkt.NameString()
	unit := am.Unit()

	mh.logger.Printf("[%s] mount request: %s (pid=%d uid=%d)",
		unit.Name, name, pkt.PID, pkt.UID)

	// Build source and target paths
	var source, target string
	if unit.AutofsType == TypeIndirect {
		// Indirect: kernel sends the subdirectory name
		source = unit.What + "/" + name
		target = filepath.Join(am.Mountpoint(), name)
	} else {
		// Direct: the mount point itself
		source = unit.What
		target = am.Mountpoint()
	}

	// Create target directory if needed
	if unit.AutofsType == TypeIndirect {
		if err := os.MkdirAll(target, unit.DirMode); err != nil {
			mh.logger.Printf("[%s] mkdir %s failed: %v", unit.Name, target, err)
			am.Fail(pkt.WaitQueueToken)
			return
		}
	}

	// Parse mount flags from options
	flags, dataOpts := parseMountFlags(unit.Options)

	// Perform the real mount
	if err := unix.Mount(source, target, unit.Type, flags, dataOpts); err != nil {
		mh.logger.Printf("[%s] mount %s → %s (%s) failed: %v",
			unit.Name, source, target, unit.Type, err)
		am.Fail(pkt.WaitQueueToken)
		return
	}

	am.TrackMount(name)
	mh.logger.Printf("[%s] mounted %s → %s (%s)", unit.Name, source, target, unit.Type)

	// Notify kernel that mount succeeded
	if err := am.Ready(pkt.WaitQueueToken); err != nil {
		mh.logger.Printf("[%s] ready notification failed: %v", unit.Name, err)
	}
}

// HandleExpire is called when the kernel expires an idle sub-mount.
// It unmounts the filesystem and notifies the kernel.
func (mh *MountHandler) HandleExpire(am *AutofsMount, pkt *V5Packet) {
	name := pkt.NameString()
	unit := am.Unit()
	target := filepath.Join(am.Mountpoint(), name)

	mh.logger.Printf("[%s] expire request: %s", unit.Name, name)

	if err := unix.Unmount(target, 0); err != nil {
		mh.logger.Printf("[%s] unmount %s failed: %v", unit.Name, target, err)
		// Try lazy unmount as fallback
		if err2 := unix.Unmount(target, unix.MNT_DETACH); err2 != nil {
			mh.logger.Printf("[%s] lazy unmount %s also failed: %v", unit.Name, target, err2)
			am.Fail(pkt.WaitQueueToken)
			return
		}
	}

	am.TrackUnmount(name)
	mh.logger.Printf("[%s] unmounted %s", unit.Name, target)

	if err := am.Ready(pkt.WaitQueueToken); err != nil {
		mh.logger.Printf("[%s] expire ready notification failed: %v", unit.Name, err)
	}
}

// HandlePacket dispatches a packet to the appropriate handler.
func (mh *MountHandler) HandlePacket(am *AutofsMount, pkt *V5Packet) error {
	if pkt.IsMissing() {
		mh.HandleMissing(am, pkt)
		return nil
	}
	if pkt.IsExpire() {
		mh.HandleExpire(am, pkt)
		return nil
	}
	return fmt.Errorf("unknown packet type %d", pkt.Type)
}

// parseMountFlags splits mount options into flag bits and remaining data string.
// Known flags (ro, nosuid, nodev, noexec, etc.) are converted to MS_* constants.
func parseMountFlags(options string) (uintptr, string) {
	if options == "" {
		return 0, ""
	}

	var flags uintptr
	var remaining []string

	for _, opt := range strings.Split(options, ",") {
		opt = strings.TrimSpace(opt)
		switch opt {
		case "ro":
			flags |= unix.MS_RDONLY
		case "nosuid":
			flags |= unix.MS_NOSUID
		case "nodev":
			flags |= unix.MS_NODEV
		case "noexec":
			flags |= unix.MS_NOEXEC
		case "sync":
			flags |= unix.MS_SYNCHRONOUS
		case "remount":
			flags |= unix.MS_REMOUNT
		case "mand":
			flags |= unix.MS_MANDLOCK
		case "dirsync":
			flags |= unix.MS_DIRSYNC
		case "noatime":
			flags |= unix.MS_NOATIME
		case "nodiratime":
			flags |= unix.MS_NODIRATIME
		case "relatime":
			flags |= unix.MS_RELATIME
		case "strictatime":
			flags |= unix.MS_STRICTATIME
		case "lazytime":
			flags |= unix.MS_LAZYTIME
		case "bind":
			flags |= unix.MS_BIND
		case "rbind":
			flags |= unix.MS_BIND | unix.MS_REC
		case "silent":
			flags |= unix.MS_SILENT
		case "rw":
			// default, no flag
		default:
			remaining = append(remaining, opt)
		}
	}

	return flags, strings.Join(remaining, ",")
}
