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

	// Perform the real mount
	if err := mountWithOptions(source, target, unit.Type, unit.Options); err != nil {
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

// stNoSymfollow is ST_NOSYMFOLLOW (statfs f_flags), which x/sys/unix
// does not define.
const stNoSymfollow = 0x2000

// perMountFlags are the flags that belong to a mount rather than to its
// superblock: the only ones a bind mount can carry, and only through a
// second, MS_REMOUNT|MS_BIND call.
const perMountFlags = unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV |
	unix.MS_NOEXEC | unix.MS_NOATIME | unix.MS_NODIRATIME |
	unix.MS_RELATIME | unix.MS_STRICTATIME | unix.MS_NOSYMFOLLOW

// atimeFlags are the mutually exclusive access-time modes.
const atimeFlags = unix.MS_NOATIME | unix.MS_RELATIME | unix.MS_STRICTATIME

// parsedOptions is an options= string split the way mount(8) splits it.
type parsedOptions struct {
	flags uintptr // MS_* bits to pass
	// named are the per-mount bits the options named either way (ro and
	// rw both name MS_RDONLY): a bind remount carries the source's value
	// of every bit not named.
	named uintptr
	data  string // what is left, for the filesystem
}

// flagOption is one option that sets or clears MS_* bits.
type flagOption struct {
	set   uintptr // bits it sets
	clear uintptr // bits it clears
}

var flagOptions = map[string]flagOption{
	"defaults":      {},
	"ro":            {set: unix.MS_RDONLY},
	"rw":            {clear: unix.MS_RDONLY},
	"nosuid":        {set: unix.MS_NOSUID},
	"suid":          {clear: unix.MS_NOSUID},
	"nodev":         {set: unix.MS_NODEV},
	"dev":           {clear: unix.MS_NODEV},
	"noexec":        {set: unix.MS_NOEXEC},
	"exec":          {clear: unix.MS_NOEXEC},
	"sync":          {set: unix.MS_SYNCHRONOUS},
	"async":         {clear: unix.MS_SYNCHRONOUS},
	"dirsync":       {set: unix.MS_DIRSYNC},
	"mand":          {set: unix.MS_MANDLOCK},
	"nomand":        {clear: unix.MS_MANDLOCK},
	"noatime":       {set: unix.MS_NOATIME, clear: atimeFlags &^ unix.MS_NOATIME},
	"atime":         {clear: unix.MS_NOATIME},
	"relatime":      {set: unix.MS_RELATIME, clear: atimeFlags &^ unix.MS_RELATIME},
	"norelatime":    {clear: unix.MS_RELATIME},
	"strictatime":   {set: unix.MS_STRICTATIME, clear: atimeFlags &^ unix.MS_STRICTATIME},
	"nostrictatime": {clear: unix.MS_STRICTATIME},
	"nodiratime":    {set: unix.MS_NODIRATIME},
	"diratime":      {clear: unix.MS_NODIRATIME},
	"lazytime":      {set: unix.MS_LAZYTIME},
	"nolazytime":    {clear: unix.MS_LAZYTIME},
	"nosymfollow":   {set: unix.MS_NOSYMFOLLOW},
	"symfollow":     {clear: unix.MS_NOSYMFOLLOW},
	"silent":        {set: unix.MS_SILENT},
	"loud":          {clear: unix.MS_SILENT},
	"bind":          {set: unix.MS_BIND},
	"rbind":         {set: unix.MS_BIND | unix.MS_REC},
	"remount":       {set: unix.MS_REMOUNT},
}

// userspaceOption reports an option that only mount(8) and fstab readers
// act on. libmount never hands these to the filesystem, and a filesystem
// that is given one fails the mount with EINVAL. user, users, owner and
// group imply nosuid,nodev,noexec only for a mount an ordinary user
// makes; slinit-mount mounts as root, as mount(8) run by root does.
func userspaceOption(opt string) bool {
	name, _, _ := strings.Cut(opt, "=")
	switch name {
	case "auto", "noauto", "nofail", "_netdev", "user", "nouser", "users",
		"owner", "group", "comment", "helper", "uhelper":
		return true
	}
	return strings.HasPrefix(opt, "x-") || strings.HasPrefix(opt, "X-")
}

// parseMountOptions splits options into MS_* bits and the data string
// for the filesystem. Of two options that contradict each other (ro and
// rw, noatime and relatime) the later one wins, as in mount(8).
func parseMountOptions(options string) parsedOptions {
	var mo parsedOptions
	var remaining []string
	for _, opt := range strings.Split(options, ",") {
		opt = strings.TrimSpace(opt)
		if opt == "" || userspaceOption(opt) {
			continue
		}
		fo, ok := flagOptions[opt]
		if !ok {
			remaining = append(remaining, opt)
			continue
		}
		mo.flags = mo.flags&^fo.clear | fo.set
		mo.named |= (fo.set | fo.clear) & perMountFlags
	}
	mo.data = strings.Join(remaining, ",")
	return mo
}

// parseMountFlags splits mount options into flag bits and remaining data string.
// Known flags (ro, nosuid, nodev, noexec, etc.) are converted to MS_* constants.
func parseMountFlags(options string) (uintptr, string) {
	mo := parseMountOptions(options)
	return mo.flags, mo.data
}

// sourceMountFlags returns the per-mount flags of the mount at path, as
// MS_* bits, the access-time mode always among them.
func sourceMountFlags(path string) (uintptr, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	var flags uintptr
	for _, m := range []struct {
		st int64
		ms uintptr
	}{
		{unix.ST_RDONLY, unix.MS_RDONLY},
		{unix.ST_NOSUID, unix.MS_NOSUID},
		{unix.ST_NODEV, unix.MS_NODEV},
		{unix.ST_NOEXEC, unix.MS_NOEXEC},
		{unix.ST_NODIRATIME, unix.MS_NODIRATIME},
		{stNoSymfollow, unix.MS_NOSYMFOLLOW},
	} {
		if st.Flags&m.st != 0 {
			flags |= m.ms
		}
	}
	switch {
	case st.Flags&unix.ST_NOATIME != 0:
		flags |= unix.MS_NOATIME
	case st.Flags&unix.ST_RELATIME != 0:
		flags |= unix.MS_RELATIME
	default:
		flags |= unix.MS_STRICTATIME
	}
	return flags, nil
}

// mountWithOptions mounts source on target as options= says.
//
// The kernel ignores every flag but MS_REC on a bind mount, so
// bind,ro,nosuid used to give a writable, setuid-honouring view of the
// source. As mount(8) does, a bind that names a per-mount flag is
// followed by a MS_REMOUNT|MS_BIND call that applies them. A bind
// remount replaces all of the mount's flags, so the bits the options do
// not name are carried over from the source: bind,ro of a nosuid mount
// stays nosuid (and where they are locked, in a user namespace, the
// remount would otherwise fail with EPERM). If the remount fails, the
// bind is taken down again rather than left weaker than asked. As with
// mount(8), the flags apply to the top mount of an rbind only.
func mountWithOptions(source, target, fstype, options string) error {
	mo := parseMountOptions(options)
	if err := unix.Mount(source, target, fstype, mo.flags, mo.data); err != nil {
		return err
	}
	if mo.flags&unix.MS_BIND == 0 || mo.flags&unix.MS_REMOUNT != 0 ||
		mo.flags&perMountFlags == 0 {
		return nil
	}
	flags := mo.flags & perMountFlags
	src, err := sourceMountFlags(target)
	if err == nil {
		carry := perMountFlags &^ mo.named
		if mo.named&atimeFlags != 0 {
			carry &^= atimeFlags
		}
		flags |= src & carry
		err = unix.Mount("", target, "", unix.MS_REMOUNT|unix.MS_BIND|flags, "")
	}
	if err != nil {
		if uerr := unix.Unmount(target, unix.MNT_DETACH); uerr != nil {
			return fmt.Errorf("apply %s to bind mount: %w (and unmount: %v)", options, err, uerr)
		}
		return fmt.Errorf("apply %s to bind mount: %w", options, err)
	}
	return nil
}
