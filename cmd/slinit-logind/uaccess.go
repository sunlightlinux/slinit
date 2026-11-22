// Device access for the user at the seat — the "uaccess" half of logind.
//
// udev tags the devices a person at the screen should be able to use
// (the GPU's card node, sound, cameras, game controllers) with "uaccess".
// systemd-logind and elogind then give the user of the seat's active
// session read/write on them with a POSIX ACL, and move it whenever the
// active session changes (seat_apply_acls). slinit-logind did neither,
// which X11 never shows — the X server runs as root — but a Wayland
// compositor does: KWin opens /dev/dri/card0 itself for its allocator,
// was refused, and Plasma's Wayland session fell back to the login
// screen unless the user was put in the video group, which gives the
// card to that user even when someone else is at the screen.
//
// The ACLs are written when the seat's active user changes, for every
// uaccess-tagged node on that seat. A device that appears later is
// handled by udev's own uaccess rule (elogind's helper, which reads the
// active user from the /run/systemd/seats record this daemon writes).
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

var (
	// uaccessEnabled is set by main for the system instance only, so a
	// user instance and the tests never touch device nodes.
	uaccessEnabled bool
	// udevRunDir is udev's runtime database: tags/uaccess lists the
	// tagged devices and data/<id> their properties (ID_SEAT).
	udevRunDir = "/run/udev"

	uaccessMu   sync.Mutex
	uaccessUser = map[string]int64{} // seat -> uid granted, -1 for none

	// Variables so the tests need neither sysfs nor real device nodes.
	devNodePathFunc   = devNodePath
	setDeviceUserFunc = setDeviceUser
)

// applySeatAccess gives uid read/write on the seat's uaccess devices and
// takes it from whoever had it. has is false when the seat has no active
// session. Root needs no entry, so it counts as none. Nothing is done
// when the seat's user has not changed since the last call.
func applySeatAccess(seat string, uid uint32, has bool) {
	if !uaccessEnabled {
		return
	}
	want := int64(-1)
	if has && uid != 0 {
		want = int64(uid)
	}
	uaccessMu.Lock()
	defer uaccessMu.Unlock()
	if prev, ok := uaccessUser[seat]; ok && prev == want {
		return
	}
	uaccessUser[seat] = want
	for _, path := range uaccessDevices(seat) {
		if err := setDeviceUserFunc(path, want); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-logind: uaccess %s: %v\n", path, err)
		}
	}
}

// uaccessDevices lists the device nodes tagged uaccess that belong to
// seat (ID_SEAT, or seat0 when unset).
func uaccessDevices(seat string) []string {
	ents, err := os.ReadDir(filepath.Join(udevRunDir, "tags", "uaccess"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		id := e.Name() // c226:0, b11:0; "+subsys:name" entries have no node
		if len(id) < 2 || (id[0] != 'c' && id[0] != 'b') {
			continue
		}
		if deviceSeat(id) != seat {
			continue
		}
		maj, min, ok := strings.Cut(id[1:], ":")
		if !ok {
			continue
		}
		path, err := devNodePathFunc(id[0], maj, min)
		if err != nil {
			continue
		}
		out = append(out, path)
	}
	return out
}

// deviceSeat reads ID_SEAT from udev's database entry for the device.
func deviceSeat(id string) string {
	b, err := os.ReadFile(filepath.Join(udevRunDir, "data", id))
	if err != nil {
		return "seat0"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "E:ID_SEAT="); ok && v != "" {
			return v
		}
	}
	return "seat0"
}

// devNodePath resolves a char ('c') or block ('b') major:minor to its
// node through sysfs, asking only the right kind: block 11:0 and char
// 11:0 are different devices.
func devNodePath(kind byte, maj, min string) (string, error) {
	if _, err := strconv.ParseUint(maj, 10, 32); err != nil {
		return "", err
	}
	if _, err := strconv.ParseUint(min, 10, 32); err != nil {
		return "", err
	}
	dir := "char"
	if kind == 'b' {
		dir = "block"
	}
	b, err := os.ReadFile(filepath.Join("/sys/dev", dir, maj+":"+min, "uevent"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "DEVNAME="); ok {
			return "/dev/" + v, nil
		}
	}
	return "", errors.New("no DEVNAME")
}

// setDeviceUser rewrites path's access ACL so uid (when non-negative) is
// the one named user with read/write.
func setDeviceUser(path string, uid int64) error {
	var acl []aclEntry
	buf := make([]byte, 4096)
	n, err := unix.Getxattr(path, aclXattr, buf)
	switch {
	case err == nil:
		if acl, err = decodeACL(buf[:n]); err != nil {
			return err
		}
	case errors.Is(err, unix.ENODATA):
		var st unix.Stat_t
		if err := unix.Stat(path, &st); err != nil {
			return err
		}
		acl = aclFromMode(st.Mode)
	default:
		return err
	}
	next, err := grantOnlyUser(acl, uid)
	if err != nil {
		return err
	}
	return unix.Setxattr(path, aclXattr, encodeACL(next), 0)
}
