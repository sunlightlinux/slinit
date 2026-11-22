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
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
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
	controller string // bus name of the caller that took control
	files      map[devKey]*os.File
	// paused marks the devices whose access is suspended because the
	// session is not in front: DRM master dropped, input revoked.
	paused map[devKey]bool
	// vtnr is the session's VT, 0 for none; vt is that VT, open while
	// this daemon holds it for the controller (vtcontrol.go).
	vtnr uint32
	vt   *os.File
}

// devNote is a PauseDevice or ResumeDevice signal owed to a controller.
// A resume carries a duplicate of the device's descriptor, so it stays
// valid until sent even if the device is released in the meantime.
type devNote struct {
	session, controller string
	member              string // "PauseDevice" or "ResumeDevice"
	key                 devKey
	fd                  *os.File
}

var (
	devicesMu sync.Mutex
	devices   = map[string]*sessionDevices{} // session id -> devices
)

// takeControl marks the session as having an active controller. systemd
// allows exactly one; `force` lets a caller displace the incumbent.
// Returning DeviceIsTaken for a second, non-forcing caller is what
// keeps two compositors from fighting over the same seat.
//
// controller is the caller's bus name. The device calls are refused to
// anyone else, as in systemd: a session's owner may run many processes, and
// only the one that took control may ask for its devices.
func takeControl(id, controller string, force bool) *dbus.Error {
	devicesMu.Lock()
	defer devicesMu.Unlock()

	if sd := devices[id]; sd != nil && sd.controlled && !force {
		return dbus.NewError("org.freedesktop.login1.Error.DeviceIsTaken",
			[]any{"session " + id + " already has a controller"})
	}
	if sd := devices[id]; sd != nil {
		// A forced take displaces the incumbent: its descriptors go
		// with it, or it would keep DRM master behind the new owner.
		closeSessionDevicesLocked(id)
	}
	devices[id] = &sessionDevices{controlled: true, controller: controller, files: map[devKey]*os.File{}}
	return nil
}

// isController reports whether sender holds control of session id.
func isController(id, sender string) bool {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	sd := devices[id]
	return sd != nil && sd.controlled && sd.controller == sender
}

func errNotController() *dbus.Error {
	return dbus.NewError("org.freedesktop.login1.Error.NotInControl",
		[]any{"You are not in control of this session"})
}

// dropController releases control of every session whose controller is
// the bus name that just went away, with the devices it was handed, and
// returns their ids. systemd-logind does the same when the controller's
// name leaves the bus (session_drop_controller): otherwise a compositor
// that dies keeps the session "taken", and every compositor started
// after it — KWin restarts itself — is refused with DeviceIsTaken.
func dropController(name string) []string {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	var ids []string
	for id, sd := range devices {
		if sd.controlled && sd.controller == name {
			closeSessionDevicesLocked(id)
			delete(devices, id)
			ids = append(ids, id)
		}
	}
	return ids
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
		delete(sd.paused, key)
	}
	if sd.vt != nil {
		restoreVT(sd.vt, sd.vtnr)
		sd.vt = nil
	}
}

// attachVT records the session's VT against its controller and, on a
// system with VTs, takes the VT for it. Failing to take it is logged, not
// returned: systemd fails TakeControl, but a compositor that worked before
// this daemon held VTs must not stop working because one cannot be held.
func (m *manager) attachVT(id, controller string, vtnr, uid uint32) {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	sd := devices[id]
	if sd == nil || !sd.controlled || sd.controller != controller {
		return
	}
	sd.vtnr = vtnr
	if !vtHandling || vtnr == 0 || sd.vt != nil {
		return
	}
	f, err := prepareVT(vtnr, uid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: session %s: cannot take tty%d, VT switches will not pause its devices: %v\n", id, vtnr, err)
		return
	}
	sd.vt = f
	m.dbgf("session %s: holding tty%d for its controller", id, vtnr)
}

// isEvdevNode reports whether path is an input event node, whose access
// is revoked while its session is in the background.
func isEvdevNode(path string) bool {
	return strings.HasPrefix(path, "/dev/input/event")
}

// eviocRevoke is EVIOCREVOKE, _IOW('E', 0x91, int): every later
// operation on the descriptor fails, for good.
const eviocRevoke = 0x40044591

// pauseDevice suspends one device's access: a DRM node keeps its
// descriptor but loses master, an input node is revoked. Other devices
// have nothing to suspend.
func pauseDevice(f *os.File) {
	switch {
	case isDRMNode(f.Name()):
		_ = drmDropMaster(f.Fd())
	case isEvdevNode(f.Name()):
		_, _, _ = unix.Syscall(unix.SYS_IOCTL, f.Fd(), eviocRevoke, 0)
	}
}

// pauseDevicesLocked pauses every device of the session not paused yet
// and returns the PauseDevice signals owed. "force", as systemd sends on
// a VT switch: the device is already paused, there is nothing to wait for.
func pauseDevicesLocked(id string, sd *sessionDevices) []devNote {
	var notes []devNote
	for key, f := range sd.files {
		if sd.paused[key] {
			continue
		}
		pauseDevice(f)
		if sd.paused == nil {
			sd.paused = map[devKey]bool{}
		}
		sd.paused[key] = true
		if sd.controlled {
			notes = append(notes, devNote{session: id, controller: sd.controller, member: "PauseDevice", key: key})
		}
	}
	return notes
}

// resumeDevicesLocked gives a session back the devices it was paused
// out of and returns the ResumeDevice signals owed. A revoked input node
// cannot be revived, so it is reopened and the new descriptor handed
// over. A device that cannot be resumed — another holder still has DRM
// master — stays paused for the next switch to retry.
func resumeDevicesLocked(id string, sd *sessionDevices) []devNote {
	var notes []devNote
	for key, f := range sd.files {
		if !sd.paused[key] {
			continue
		}
		switch {
		case isDRMNode(f.Name()):
			if err := drmSetMaster(f.Fd()); err != nil {
				continue
			}
		case isEvdevNode(f.Name()):
			g, err := openDevice(f.Name())
			if err != nil {
				continue
			}
			_ = f.Close()
			sd.files[key] = g
			f = g
		}
		delete(sd.paused, key)
		if !sd.controlled {
			continue
		}
		dup, err := dupFile(f)
		if err != nil {
			continue
		}
		notes = append(notes, devNote{session: id, controller: sd.controller, member: "ResumeDevice", key: key, fd: dup})
	}
	return notes
}

func dupFile(f *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

// vtIsFront reports whether a session on vtnr is the one in front. As in
// sessionIsActive, a session without a VT, or a seat whose foreground is
// unknown, counts as in front.
func vtIsFront(vtnr uint32) bool {
	vt := currentVT.Load()
	return vtnr == 0 || vt == 0 || vtnr == vt
}

// syncDevicesToVT pauses the devices of every session that is no longer
// in front and resumes those of the one that now is — seat_set_active's
// half of a VT switch. Called whenever the foreground VT changes.
func (m *manager) syncDevicesToVT() {
	devicesMu.Lock()
	var notes []devNote
	for id, sd := range devices {
		if vtIsFront(sd.vtnr) {
			notes = append(notes, resumeDevicesLocked(id, sd)...)
		} else {
			notes = append(notes, pauseDevicesLocked(id, sd)...)
		}
	}
	devicesMu.Unlock()
	m.sendDevNotes(notes)
}

// sendDevNoteFunc delivers one note; a variable so the tests can watch.
var sendDevNoteFunc = (*manager).sendDevNote

func (m *manager) sendDevNotes(notes []devNote) {
	for _, n := range notes {
		if err := sendDevNoteFunc(m, n); err != nil {
			m.dbgf("session %s: %s %d:%d to %s: %v", n.session, n.member, n.key.major, n.key.minor, n.controller, err)
		}
		if n.fd != nil {
			_ = n.fd.Close()
		}
	}
}

// sendDevNote emits the signal to the controller alone. A broadcast
// would hand a resumed input descriptor — a keyboard — to every client
// on the bus that listens for it.
func (m *manager) sendDevNote(n devNote) error {
	return m.conn.Send(devNoteMessage(n), nil).Err
}

func devNoteMessage(n devNote) *dbus.Message {
	var body []any
	if n.member == "ResumeDevice" {
		body = []any{n.key.major, n.key.minor, dbus.UnixFD(n.fd.Fd())}
	} else {
		body = []any{n.key.major, n.key.minor, "force"}
	}
	return &dbus.Message{
		Type: dbus.TypeSignal,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:        dbus.MakeVariant(sessionPath(n.session)),
			dbus.FieldInterface:   dbus.MakeVariant(sessionIface),
			dbus.FieldMember:      dbus.MakeVariant(n.member),
			dbus.FieldDestination: dbus.MakeVariant(n.controller),
			dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body...)),
		},
		Body: body,
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
//
// A session that is not in front gets its device paused from the start
// and is told so with inactive=true, as in systemd: claiming DRM master
// for it would take the screen from the session that is in front.
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
		return dbus.UnixFD(existing.Fd()), sd.paused[key], nil
	}

	f, err := openDevice(path)
	if err != nil {
		return 0, false, dbus.NewError("org.freedesktop.login1.Error.DeviceOpenFailed",
			[]any{path + ": " + err.Error()})
	}

	if !vtIsFront(sd.vtnr) {
		pauseDevice(f)
		if sd.paused == nil {
			sd.paused = map[devKey]bool{}
		}
		sd.paused[key] = true
		sd.files[key] = f
		return dbus.UnixFD(f.Fd()), true, nil
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
		delete(sd.paused, key)
	}
}

// vanishedName returns the unique bus name a NameOwnerChanged signal
// reports gone — a name with an old owner and no new one — or "".
// Well-known names change hands; only a unique name leaving means its
// connection, and so the process behind it, is gone.
func vanishedName(sig *dbus.Signal) string {
	if sig == nil || sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) != 3 {
		return ""
	}
	name, ok1 := sig.Body[0].(string)
	newOwner, ok2 := sig.Body[2].(string)
	if !ok1 || !ok2 || newOwner != "" || len(name) == 0 || name[0] != ':' {
		return ""
	}
	return name
}

// watchControllers releases a session's control when its controller
// leaves the bus. It runs for the life of the daemon.
func (m *manager) watchControllers() {
	if err := m.conn.AddMatchSignal(
		dbus.WithMatchObjectPath("/org/freedesktop/DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
	); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: cannot watch bus names, a dead compositor will keep its session taken: %v\n", err)
		return
	}
	ch := make(chan *dbus.Signal, 64)
	m.conn.Signal(ch)
	for sig := range ch {
		name := vanishedName(sig)
		if name == "" {
			continue
		}
		for _, id := range dropController(name) {
			m.dbgf("session %s: controller %s left the bus, control released", id, name)
		}
	}
}
