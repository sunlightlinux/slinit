package main

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// Caller authorization.
//
// slinit-logind used to check no caller at all: PowerOff, KillUser and the
// rest trusted whoever the bus let through, so the D-Bus policy was the only
// barrier, and a policy wide enough for a desktop session would have let any
// local user power the machine off or kill another user's processes.
//
// There is no polkit here, so the rules are the ones systemd-logind applies
// when built without it (src/shared/bus-polkit.c, src/login/logind-*-dbus.c
// at 87da7901bb): a call is allowed from root, or from its "good user" — the
// owner of the session or the user it acts on — and denied otherwise. A few
// calls carry their own rule (TakeControl, the device calls, ReleaseSession,
// the VT switches); they say so where they are.

// noGoodUser marks a call only root may make.
const noGoodUser int64 = -1

// senderUID resolves the uid behind a bus name. A variable so tests can stand
// in for the bus daemon.
var senderUID = func(m *manager, sender dbus.Sender) (uint32, error) {
	return m.callerUID(string(sender))
}

func errAccessDenied(format string, args ...any) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.AccessDenied",
		[]any{fmt.Sprintf(format, args...)})
}

// authorize allows the call from root or from goodUser. A caller whose uid
// cannot be learnt is refused: failing open here is what this file exists to
// stop.
func (m *manager) authorize(sender dbus.Sender, goodUser int64, what string) *dbus.Error {
	uid, err := senderUID(m, sender)
	if err != nil {
		m.dbgf("%s: refused %s, uid unknown: %v", what, sender, err)
		return errAccessDenied("%s: cannot identify the caller", what)
	}
	if uid == 0 || (goodUser >= 0 && int64(uid) == goodUser) {
		return nil
	}
	m.dbgf("%s: refused uid %d (%s)", what, uid, sender)
	return errAccessDenied("%s: access denied", what)
}

// authorizeSessionOwner allows root and the user owning session id.
func (m *manager) authorizeSessionOwner(sender dbus.Sender, id, what string) *dbus.Error {
	if !validSessionID(id) {
		return errBadSessionID(id)
	}
	var rec SessionRecord
	if !readJSON(sessionFile(id), &rec) {
		return dbus.NewError("org.freedesktop.login1.Error.NoSuchSession", []any{id})
	}
	return m.authorize(sender, int64(rec.UserID), what)
}
