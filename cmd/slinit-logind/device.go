// Session device delegation — the TakeControl / TakeDevice half of
// org.freedesktop.login1.Session.
//
// A Wayland compositor does not open /dev/dri/card0 itself. It asks
// logind to open the node and pass the descriptor back, because logind
// is what arbitrates DRM master between the sessions sharing a seat.
// Under X11 none of this matters — the X server runs privileged and
// opens the node directly — which is why gdm's Xorg greeter comes up on
// Sunlight while the Wayland one stalls after "Boot VGA GPU selected as
// primary".
//
// Previously TakeControl and ReleaseDevice were `return nil` and
// TakeDevice opened the node and deliberately leaked our copy of the
// descriptor. That leak is not cosmetic: while we hold an open DRM fd
// that was granted master, the next compositor to come along contends
// with a descriptor nobody can close. Devices are tracked per session
// here so every one of them has an owner and a release path.
package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

// devKey identifies a device by the major:minor pair the client asked
// for, which is what ReleaseDevice and PauseDeviceComplete quote back.
type devKey struct {
	major uint32
	minor uint32
}

// sessionDevices holds the descriptors handed to one session's
// controller, plus whether anyone has claimed control at all.
type sessionDevices struct {
	controlled bool
	files      map[devKey]*os.File
}

var (
	devicesMu sync.Mutex
	devices   = map[string]*sessionDevices{} // session id -> devices
)

// takeControl marks the session as having an active controller. systemd
// allows exactly one; `force` lets a caller displace the incumbent.
// Returning DeviceIsTaken for a second, non-forcing caller is what
// keeps two compositors from fighting over the same seat.
func takeControl(id string, force bool) *dbus.Error {
	devicesMu.Lock()
	defer devicesMu.Unlock()

	if sd := devices[id]; sd != nil && sd.controlled && !force {
		return dbus.NewError("org.freedesktop.login1.Error.DeviceIsTaken",
			[]any{"session " + id + " already has a controller"})
	}
	devices[id] = &sessionDevices{controlled: true, files: map[devKey]*os.File{}}
	return nil
}

// releaseControl drops every descriptor the session was handed and
// releases DRM master with them, so the next compositor can claim it.
func releaseControl(id string) {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	closeSessionDevicesLocked(id)
	delete(devices, id)
}

func closeSessionDevicesLocked(id string) {
	sd := devices[id]
	if sd == nil {
		return
	}
	for key, f := range sd.files {
		dropAndClose(f)
		delete(sd.files, key)
	}
}

// dropAndClose releases DRM master (best effort — the fd may not be a
// DRM node, in which case the ioctl fails harmlessly) and closes.
func dropAndClose(f *os.File) {
	_ = drmDropMaster(f.Fd())
	_ = f.Close()
}

// takeDevice opens major:minor for the session and returns the
// descriptor to hand over the bus, together with the `inactive` flag
// systemd's protocol carries.
//
// The returned *os.File stays open on our side on purpose: godbus dups
// the descriptor when it marshals the reply, and logind is expected to
// retain its own copy so it can drop master or revoke access on a VT
// switch. The copy is registered against the session so ReleaseDevice
// and ReleaseControl can close it.
func takeDevice(id string, major, minor uint32) (dbus.UnixFD, bool, *dbus.Error) {
	path, err := devPathForMajorMinor(major, minor)
	if err != nil {
		return 0, false, dbus.NewError("org.freedesktop.login1.Error.NoSuchDevice",
			[]any{fmt.Sprintf("%d:%d: %v", major, minor, err)})
	}

	devicesMu.Lock()
	defer devicesMu.Unlock()

	sd := devices[id]
	if sd == nil {
		// Tolerate a TakeDevice that skipped TakeControl. systemd
		// refuses it, but refusing here would regress the X11 path
		// that works today, and we gain nothing by being stricter
		// than the clients we support.
		sd = &sessionDevices{files: map[devKey]*os.File{}}
		devices[id] = sd
	}
	key := devKey{major, minor}
	if existing, ok := sd.files[key]; ok {
		// Re-take of a device we already hold: hand back the same
		// descriptor rather than opening a second master-holding fd.
		return dbus.UnixFD(existing.Fd()), false, nil
	}

	f, err := openDevice(path)
	if err != nil {
		return 0, false, dbus.NewError("org.freedesktop.login1.Error.DeviceOpenFailed",
			[]any{path + ": " + err.Error()})
	}

	if isDRMNode(path) {
		// Claim master explicitly. The open above may already have
		// been granted it implicitly (kernel gives master to the
		// first opener), but that depends on who else has the node
		// open and is exactly the race elogind retries around.
		if err := drmSetMaster(f.Fd()); err != nil {
			_ = f.Close()
			return 0, false, dbus.NewError("org.freedesktop.login1.Error.DeviceOpenFailed",
				[]any{path + ": drm set-master: " + err.Error()})
		}
	}

	sd.files[key] = f
	return dbus.UnixFD(f.Fd()), false, nil
}

// releaseDevice closes our copy of one device, dropping DRM master with
// it. The client keeps its own dup until it closes that itself.
func releaseDevice(id string, major, minor uint32) {
	devicesMu.Lock()
	defer devicesMu.Unlock()

	sd := devices[id]
	if sd == nil {
		return
	}
	key := devKey{major, minor}
	if f, ok := sd.files[key]; ok {
		dropAndClose(f)
		delete(sd.files, key)
	}
}
