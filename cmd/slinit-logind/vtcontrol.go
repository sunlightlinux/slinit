// The VT of a session that has a controller, held the way systemd-logind
// holds it (session_prepare_vt, session_leave_vt, session_restore_vt).
//
// A Wayland compositor does not touch the VT: it calls TakeControl and
// leaves the VT to logind. slinit-logind used to leave it to nobody, so
// the VT kept whatever mode the display manager had set. On sl-demo1
// SDDM's session helper had put Plasma's VT in VT_PROCESS with its own
// signals; "Switch User" asked the kernel for another VT, the kernel
// signalled the helper to release this one, and the release never came.
// The new greeter's X server sat in VT_WAITACTIVE for good, and KWin
// kept DRM master besides, so it could not have drawn anyway.
//
// So, from TakeControl until control is released, the VT is ours:
// VT_PROCESS with a real-time release signal, KD_GRAPHICS, and the
// console keyboard off so keystrokes reach only the compositor. When the
// kernel asks to leave, the session's devices are paused — DRM master
// dropped, input revoked, the controller told with PauseDevice — and the
// switch acknowledged. The other half, resuming, follows the foreground
// VT (syncDevicesToVT, from watchActiveVT).
package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// From <linux/kd.h> and <linux/vt.h>.
const (
	kdSetMode  = 0x4B3A
	kdText     = 0x00
	kdGraphics = 0x01
	kdSkbMode  = 0x4B45
	kXlate     = 0x01
	kUnicode   = 0x03
	kOff       = 0x04
	vtSetMode  = 0x5602
	vtRelDisp  = 0x5605
	vtAuto     = 0x00
	vtProcess  = 0x01
)

// The release and acquire signals. systemd uses glibc's SIGRTMIN and
// SIGRTMIN+1, 34 and 35, but the kernel sends whatever the VT is set to,
// and Go reserves 32 to 34: signal.Notify on 34 does nothing, without an
// error, and the runtime then dies of the signal. That is how the first
// build of this killed slinit-logind on every Switch User. 35 and 36 are
// the first two Go delivers; TestVTSignalsAreDelivered sends them.
var (
	vtRelSig = syscall.Signal(35)
	vtAcqSig = syscall.Signal(36)
)

// vtHandling is set by main for the system instance only, so a user
// instance and the tests never change a VT's mode.
var vtHandling bool

// vtModeArg is struct vt_mode.
type vtModeArg struct {
	mode   int8
	waitv  int8
	relsig int16
	acqsig int16
	frsig  int16
}

func ioctlInt(f *os.File, req, arg uintptr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), req, arg); errno != 0 {
		return errno
	}
	return nil
}

func setVTMode(f *os.File, mode int8, rel, acq syscall.Signal) error {
	arg := vtModeArg{mode: mode, relsig: int16(rel), acqsig: int16(acq)}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), vtSetMode, uintptr(unsafe.Pointer(&arg))); errno != 0 {
		return errno
	}
	return nil
}

func openVT(vtnr uint32) (*os.File, error) {
	return os.OpenFile("/dev/tty"+strconv.FormatUint(uint64(vtnr), 10),
		os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
}

// prepareVT takes vtnr for a session owned by uid and returns the open
// VT. On failure the VT is put back as it was found as far as it can be.
func prepareVT(vtnr, uid uint32) (*os.File, error) {
	f, err := openVT(vtnr)
	if err != nil {
		return nil, err
	}
	step := func(what string, err error) error {
		if err == nil {
			return nil
		}
		restoreVT(f, vtnr)
		return fmt.Errorf("%s: %w", what, err)
	}
	if err := step("chown", f.Chown(int(uid), -1)); err != nil {
		return nil, err
	}
	if err := step("KDSKBMODE K_OFF", ioctlInt(f, kdSkbMode, kOff)); err != nil {
		return nil, err
	}
	if err := step("KDSETMODE KD_GRAPHICS", ioctlInt(f, kdSetMode, kdGraphics)); err != nil {
		return nil, err
	}
	// VT_AUTO does not work with KD_GRAPHICS: nothing would ever let
	// the VT go. Hence VT_PROCESS, acknowledged in leaveVT.
	if err := step("VT_SETMODE VT_PROCESS", setVTMode(f, vtProcess, vtRelSig, vtAcqSig)); err != nil {
		return nil, err
	}
	return f, nil
}

// restoreVT puts a VT back in text mode with automatic switching, gives
// it back to root, and closes f. A VT whose descriptor was hung up
// answers EIO; it is retried through a fresh one, as systemd does.
func restoreVT(f *os.File, vtnr uint32) {
	if err := resetVT(f); errors.Is(err, unix.EIO) {
		if g, err := openVT(vtnr); err == nil {
			_ = resetVT(g)
			_ = g.Close()
		}
	}
	_ = f.Close()
}

func resetVT(f *os.File) error {
	err := ioctlInt(f, kdSetMode, kdText)
	kb := uintptr(kXlate)
	if b, rerr := os.ReadFile("/sys/module/vt/parameters/default_utf8"); rerr != nil || strings.TrimSpace(string(b)) != "0" {
		kb = kUnicode
	}
	_ = ioctlInt(f, kdSkbMode, kb)
	_ = setVTMode(f, vtAuto, 0, 0)
	_ = f.Chown(0, -1)
	_ = f.Chmod(0620)
	return err
}

// releaseVT acknowledges the kernel's request to switch away from vtnr,
// retrying through a fresh descriptor if f was hung up.
func releaseVT(f *os.File, vtnr uint32) error {
	err := ioctlInt(f, vtRelDisp, 1)
	if errors.Is(err, unix.EIO) {
		var g *os.File
		if g, err = openVT(vtnr); err == nil {
			err = ioctlInt(g, vtRelDisp, 1)
			_ = g.Close()
		}
	}
	return err
}

// startVTSignals answers the kernel's VT switch requests for the life of
// the daemon. It subscribes before returning, so no VT can be set to
// signal this process before something listens: a lost release signal
// leaves the switch waiting for good. Acquire signals are taken and
// dropped: systemd ignores them too, the switch completes without an
// acknowledgement, and resuming follows the foreground VT instead.
func (m *manager) startVTSignals() {
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, vtRelSig, vtAcqSig)
	go func() {
		for sig := range ch {
			if sig == vtRelSig {
				m.leaveVT()
			}
		}
	}()
}

// leaveVT pauses the devices of the session on the foreground VT and lets
// the kernel switch away from it. A release request for a VT no session
// holds is acknowledged anyway and the VT reset, or the seat would be
// stuck on it, as systemd's manager_vt_switch reasons.
func (m *manager) leaveVT() {
	vt, ok := readActiveVT()
	if !ok {
		return
	}
	devicesMu.Lock()
	var notes []devNote
	found := false
	for id, sd := range devices {
		if sd.vt == nil || sd.vtnr != vt {
			continue
		}
		found = true
		notes = pauseDevicesLocked(id, sd)
		if err := releaseVT(sd.vt, vt); err != nil {
			// The switch is not happening; do not leave the
			// session that stays in front without its devices.
			m.dbgf("session %s: VT_RELDISP on tty%d: %v", id, vt, err)
			notes = append(notes, resumeDevicesLocked(id, sd)...)
		} else {
			m.dbgf("session %s: leaving tty%d, %d device(s) paused", id, vt, len(notes))
		}
		break
	}
	devicesMu.Unlock()
	m.sendDevNotes(notes)

	if !found {
		m.dbgf("release request for tty%d, which no session holds; releasing it", vt)
		if f, err := openVT(vt); err == nil {
			_ = ioctlInt(f, vtRelDisp, 1)
			restoreVT(f, vt)
		}
	}
}
