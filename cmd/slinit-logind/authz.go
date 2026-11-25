package main

import (
	"fmt"
	"os"
	"strings"

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
// the VT switches); they say so where they are. The VT switches alone ask
// polkit as well (mayChvt).

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

// mayPower is the one rule for the power and sleep calls, used both by
// the actions and by the Can* probes that predict them, so the two cannot
// disagree.
//
// Root may make every one of them. Beyond root, the rule is polkit's
// default for org.freedesktop.login1, which is what lets a desktop user
// press Power Off: power-off, reboot, suspend and hibernate are allow_active
// = yes, and so are their -multiple-sessions forms, so the user of the
// active local session may make them whoever else is logged in. halt is
// auth_admin_keep even for the active session, and so is every
// -ignore-inhibit form; there is no authentication agent here, so those
// stay root's. "Active" is polkit's sense, judged on the session of the
// calling process: local (it has a seat, and is not remote) and the
// foreground session of that seat.
//
// Everything this cannot establish is a refusal: an unknown caller, a
// process in no session or in someone else's, a seat whose foreground
// cannot be read.
func (m *manager) mayPower(sender dbus.Sender, what string) *dbus.Error {
	uid, err := senderUID(m, sender)
	if err != nil {
		m.dbgf("%s: refused %s, uid unknown: %v", what, sender, err)
		return errAccessDenied("%s: cannot identify the caller", what)
	}
	if uid == 0 {
		return nil
	}
	rule, ok := powerRules[what]
	if !ok || !rule.activeMay {
		m.dbgf("%s: refused uid %d, root only", what, uid)
		return errAccessDenied("%s: access denied", what)
	}
	if m.inhibitors != nil {
		if in := m.inhibitors.blockedBy(rule.class); in != nil {
			m.dbgf("%s: refused uid %d, blocked by %q", what, uid, in.Who)
			return errAccessDenied("%s: blocked by an inhibitor held by %s", what, in.Who)
		}
	}
	if id, why := m.callerActiveLocalSession(sender, uid); id == "" {
		m.dbgf("%s: refused uid %d: %s", what, uid, why)
		return errAccessDenied("%s: access denied", what)
	}
	return nil
}

// chvtAction is polkit's action for switching VTs.
const chvtAction = "org.freedesktop.login1.chvt"

// mayChvt is the rule for the calls that switch the VT of seat:
// ActivateSession, ActivateSessionOnSeat, Session.Activate,
// Seat.ActivateSession and Seat.SwitchTo. systemd checks polkit's
// org.freedesktop.login1.chvt for each, whose default is allow_active =
// yes and auth_admin_keep otherwise. They used to be open to anyone, so
// an ssh user could flip the console, or bring their own inactive local
// session to the front and so gain the active session's power rights.
//
// Root may. So may the user of the session in the foreground of seat,
// judged as mayPower judges it, which is who switches VTs on a desktop:
// GDM's daemon runs as root, and the compositor and the greeter run in
// the active session. Anyone else is polkit's to decide, without
// interaction; without polkitd the answer is no.
func (m *manager) mayChvt(sender dbus.Sender, seat, what string) *dbus.Error {
	uid, err := senderUID(m, sender)
	if err != nil {
		m.dbgf("%s: refused %s, uid unknown: %v", what, sender, err)
		return errAccessDenied("%s: cannot identify the caller", what)
	}
	if uid == 0 {
		return nil
	}
	id, why := m.callerActiveLocalSession(sender, uid)
	if id != "" {
		var rec SessionRecord
		if readJSON(sessionFile(id), &rec) && rec.SeatID == seat {
			return nil
		}
		why = "caller's session is on another seat"
	}
	if m.polkit == nil {
		m.dbgf("%s: refused uid %d: %s", what, uid, why)
		return errAccessDenied("%s: access denied", what)
	}
	if err := m.polkit.Authorize(sender, chvtAction, nil, false); err != nil {
		m.dbgf("%s: refused uid %d: %s, and polkit: %s", what, uid, why, err.Name)
		return err
	}
	return nil
}

// powerRules maps each power and sleep call to the inhibitor class that
// can block it and to polkit's allow_active for it.
var powerRules = map[string]struct {
	class     string
	activeMay bool
}{
	"poweroff":             {"shutdown", true},
	"reboot":               {"shutdown", true},
	"halt":                 {"shutdown", false},
	"Suspend":              {"sleep", true},
	"Hibernate":            {"sleep", true},
	"HybridSleep":          {"sleep", true},
	"SuspendThenHibernate": {"sleep", true},
	"Sleep":                {"sleep", true},
}

// senderPID and sessionOfPID are variables so tests can stand in for the
// bus daemon and for /proc.
var (
	senderPID = func(m *manager, sender dbus.Sender) (uint32, error) {
		return m.callerPID(string(sender))
	}
	sessionOfPID = func(pid uint32) string {
		if id, _ := findSessionByLeaderLocked(pid); id != "" {
			return id
		}
		return findSessionByCgroup(pid)
	}
)

// callerActiveLocalSession returns the id of the session the calling
// process belongs to when that session is uid's own, local and in the
// foreground of its seat, and otherwise "" with the reason.
//
// The session must be uid's own because the process is found by pid, and
// a pid can be reused between the bus naming it and this lookup: with the
// uid check, a reused pid can only land in a session of the same user.
func (m *manager) callerActiveLocalSession(sender dbus.Sender, uid uint32) (string, string) {
	rec, why := m.callerLocalSession(sender, uid)
	if rec == nil {
		return "", why
	}
	// sessionIsActive calls a session with no VT active, which is right
	// for what a desktop is shown and wrong for granting power: here the
	// foreground has to be known, and this has to be the session in it.
	if currentVT.Load() == 0 {
		return "", "foreground VT unknown"
	}
	active, ok := activeSessionOnSeat(loadSessionRecords(), rec.SeatID)
	if !ok || active.ID != rec.ID {
		return "", "caller's session is not in the foreground"
	}
	return rec.ID, ""
}

// callerLocalSession returns the session record of the calling process
// when it is uid's own and local (it has a seat and is not remote),
// whether or not it is in the foreground: polkit's allow_inactive. It
// returns nil and the reason otherwise.
func (m *manager) callerLocalSession(sender dbus.Sender, uid uint32) (*SessionRecord, string) {
	pid, err := senderPID(m, sender)
	if err != nil || pid == 0 {
		return nil, "caller pid unknown"
	}
	id := sessionOfPID(pid)
	if id == "" {
		return nil, "caller is in no session"
	}
	var rec SessionRecord
	if !readJSON(sessionFile(id), &rec) {
		return nil, "caller's session has no record"
	}
	if rec.UserID != uid {
		return nil, "caller's session belongs to another user"
	}
	if rec.Remote || rec.SeatID == "" {
		return nil, "caller's session is not local"
	}
	return &rec, ""
}

// mayInhibit is polkit's default for org.freedesktop.login1.inhibit-*,
// for a caller other than root:
//
//   - a delay lock on shutdown or sleep, and a block on idle, are
//     allow_any = yes: anyone may take one. A delay lock only buys the
//     holder inhibitDelayMax before the action goes ahead anyway.
//   - a block on shutdown or sleep, and the handle-* key locks, are
//     allow_inactive/allow_active = yes, allow_any no or auth_admin: the
//     user of a local session, in the foreground or not, may take one.
//
// Root's own power calls are not held back by a user's block lock; it
// refuses other users' power calls, as polkit's -ignore-inhibit rule does.
// This used to be root only, so gnome-session could not take the delay
// lock it locks the screen under before a suspend ("Failed to get delay
// inhibitor: access denied"), nor gsd-power take over the power key.
func (m *manager) mayInhibit(sender dbus.Sender, what, mode string) *dbus.Error {
	uid, err := senderUID(m, sender)
	if err != nil {
		m.dbgf("Inhibit: refused %s, uid unknown: %v", sender, err)
		return errAccessDenied("Inhibit: cannot identify the caller")
	}
	if uid == 0 {
		return nil
	}
	needLocal := false
	for _, tok := range strings.Split(what, ":") {
		switch {
		case tok == "idle", mode == "delay" && (tok == "shutdown" || tok == "sleep"):
		default:
			needLocal = true
		}
	}
	if !needLocal {
		return nil
	}
	if rec, why := m.callerLocalSession(sender, uid); rec == nil {
		m.dbgf("Inhibit %s/%s: refused uid %d: %s", what, mode, uid, why)
		return errAccessDenied("Inhibit: access denied")
	}
	return nil
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

// noteUserPower leaves a line on stderr when someone other than root
// powers off or sleeps the machine, so the journal says who did. Root's
// calls are the administrator's own and were never logged here.
func (m *manager) noteUserPower(sender dbus.Sender, what string) {
	uid, err := senderUID(m, sender)
	if err != nil || uid == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "slinit-logind: %s requested by uid %d (%s), user of the active session\n", what, uid, sender)
}
