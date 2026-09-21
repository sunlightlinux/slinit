// Session activity follows the foreground VT, the way elogind decides it.
//
// Until this existed every session reported Active=yes forever, and the
// greeter could not come back. gnome-shell fades its login dialog to
// opacity 0 when it starts a user session, and fades it back in only
// when its own session's Active property changes to true
// (loginDialog.js, _getGreeterSessionProxy). A session that is always
// active never changes, so after a quick logout GDM re-used the old
// greeter and it sat on the screen showing nothing but its background.
//
// The rule, from elogind's seat_active_vt_changed: on a seat with VTs,
// the active session is the one whose VT is in the foreground. Anything
// without a seat (ssh, cron) is always active.
package main

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"golang.org/x/sys/unix"
)

// activeVTPath is the kernel's record of the foreground VT. It supports
// poll(): the attribute is sysfs_notify'd on every switch.
const activeVTPath = "/sys/class/tty/tty0/active"

// vtActivateIoctl is VT_ACTIVATE from <linux/vt.h>.
const vtActivateIoctl = 0x5606

// currentVT is the foreground VT number, or 0 when it is unknown (no
// VTs, e.g. a container). Zero keeps the old behaviour of reporting
// every session active, which is the only safe answer without data.
var currentVT atomic.Uint32

// parseActiveVT turns the attribute's "tty7\n" into 7.
func parseActiveVT(s string) (uint32, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "tty")
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint32(n), true
}

func readActiveVT() (uint32, bool) {
	b, err := os.ReadFile(activeVTPath)
	if err != nil {
		return 0, false
	}
	return parseActiveVT(string(b))
}

// sessionIsActive reports whether rec is the foreground session.
//
// A seat0 session with no VT number is deliberately reported active.
// elogind would call it inactive, but we have no way to prove such a
// session is in the background, and marking a live desktop inactive
// costs it polkit's allow_active and GDM's session lookup.
func sessionIsActive(rec SessionRecord) bool {
	vt := currentVT.Load()
	if vt == 0 || rec.SeatID == "" || rec.VTNr == 0 {
		return true
	}
	return rec.VTNr == vt
}

func sessionState(rec SessionRecord) string {
	if sessionIsActive(rec) {
		return "active"
	}
	return "online"
}

// activeSessionOnSeat picks the seat's foreground session: the one on
// the foreground VT, newest first if two share it. With the VT unknown
// it falls back to the newest session, which is what the seat file
// reported before VT tracking existed.
func activeSessionOnSeat(recs []SessionRecord, seatID string) (SessionRecord, bool) {
	vt := currentVT.Load()
	var found SessionRecord
	ok := false
	for _, r := range recs {
		if r.SeatID != seatID {
			continue
		}
		if vt == 0 || r.VTNr == vt {
			found, ok = r, true
		}
	}
	return found, ok
}

// refreshActiveLocked brings every view of session activity in line
// with currentVT: each session's Active/State properties, the compat
// records libelogind reads, and the seat's ActiveSession/Sessions.
// Called with sessionMu held.
//
// A property is only written when it changed. prop.SetMust emits
// PropertiesChanged unconditionally, and a spurious Active=true is
// exactly the edge gnome-shell's greeter reacts to.
func (m *manager) refreshActiveLocked() {
	recs := loadSessionRecords()
	for _, rec := range recs {
		_ = writeCompatSessionFile(rec)
		m.setSessionPropIfChanged(rec.ID, "Active", sessionIsActive(rec))
		m.setSessionPropIfChanged(rec.ID, "State", sessionState(rec))
	}
	rewriteCompatAggregates()
	m.refreshSeatProps(recs)
}

func (m *manager) setSessionPropIfChanged(id, name string, value any) {
	m.mu.RLock()
	props := m.sessionProps[id]
	m.mu.RUnlock()
	if props == nil {
		return
	}
	setPropIfChanged(props, sessionIface, name, value)
}

// refreshSeatProps updates Seat.ActiveSession and Seat.Sessions, which
// were exported once at startup and then never touched: GetAll on
// seat0 answered "no sessions, none active" while two were running.
func (m *manager) refreshSeatProps(recs []SessionRecord) {
	m.mu.RLock()
	seats := make(map[string]*prop.Properties, len(m.seatProps))
	for id, p := range m.seatProps {
		seats[id] = p
	}
	m.mu.RUnlock()

	for id, props := range seats {
		active := seatActiveTuple("")
		if rec, ok := activeSessionOnSeat(recs, id); ok {
			active = seatActiveTuple(rec.ID)
		}
		sessions := []SessionRef{}
		for _, r := range recs {
			if r.SeatID == id {
				sessions = append(sessions, SessionRef{ID: r.ID, Path: sessionPath(r.ID)})
			}
		}
		setPropIfChanged(props, seatIface, "ActiveSession", active)
		setPropIfChanged(props, seatIface, "Sessions", sessions)
	}
}

func setPropIfChanged(props *prop.Properties, iface, name string, value any) {
	if reflect.DeepEqual(props.GetMust(iface, name), value) {
		return
	}
	props.SetMust(iface, name, value)
}

// watchActiveVT follows VT switches for the life of the daemon.
//
// sysfs signals a change with POLLPRI|POLLERR, and only re-arms once the
// attribute has been read again from offset 0 — so every wakeup is
// followed by a fresh pread, which is also what makes the first pass
// catch a switch that raced startup.
func (m *manager) watchActiveVT() {
	f, err := os.Open(activeVTPath)
	if err != nil {
		m.dbgf("no VT tracking (%v): every session stays active", err)
		return
	}
	defer f.Close()

	buf := make([]byte, 32)
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLPRI | unix.POLLERR}}
	for {
		n, err := f.ReadAt(buf, 0)
		if err != nil && err != io.EOF {
			m.dbgf("reading %s: %v; VT tracking stopped", activeVTPath, err)
			return
		}
		if vt, ok := parseActiveVT(string(buf[:n])); ok && vt != currentVT.Load() {
			m.dbgf("foreground VT is now %d", vt)
			currentVT.Store(vt)
			sessionMu.Lock()
			m.refreshActiveLocked()
			sessionMu.Unlock()
		}
		for {
			_, err := unix.Poll(fds, -1)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				m.dbgf("poll %s: %v; VT tracking stopped", activeVTPath, err)
				return
			}
			break
		}
	}
}

// activateVT asks the kernel to bring vt to the foreground. The switch
// completes asynchronously — the current owner may have to release the
// VT first — and watchActiveVT reports it when it does, so there is
// nothing to wait for here.
func activateVT(vt uint32) error {
	f, err := os.OpenFile("/dev/tty0", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), vtActivateIoctl, uintptr(vt)); errno != 0 {
		return errno
	}
	return nil
}

// activateSession brings a session to the foreground by switching to
// its VT. A session without one (no seat, or no VT number) has nothing
// to switch to, and is already reported active.
func activateSession(rec SessionRecord) *dbus.Error {
	if rec.SeatID == "" || rec.VTNr == 0 {
		return nil
	}
	if err := activateVT(rec.VTNr); err != nil {
		return dbus.NewError("org.freedesktop.DBus.Error.Failed",
			[]any{fmt.Sprintf("switch to VT %d: %v", rec.VTNr, err)})
	}
	return nil
}
