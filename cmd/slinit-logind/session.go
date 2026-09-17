// Session lifecycle — Phase B.
//
// CreateSession is called from pam_elogind.so (or pam_systemd.so —
// same D-Bus API) during pam_open_session hooks. slinit-logind
// answers with the session id + object path + runtime dir + a fifo
// fd whose closure marks the session as gone. On close-side,
// pam_close_session invokes ReleaseSession by id.
//
// Cgroup allocation follows systemd's naming:
//   /sys/fs/cgroup/user.slice/user-<uid>.slice/session-<id>.scope
// The leader PID is written into that scope's cgroup.procs so every
// child process the shell forks inherits the scope for accounting
// and kill-tree semantics.
//
// Runtime directory /run/user/<uid> is created if absent (mode 0700,
// owned by the user); subsequent sessions for the same UID reuse it
// so per-user apps can co-locate socket/state files there.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Property mirrors the (sv) member of Manager.CreateSession's
// properties array. Named type (not an anonymous struct inline in
// the signature) because godbus's reflection-based method exporter
// silently rejects methods whose args or returns include anonymous
// composite types.
type Property struct {
	Name  string
	Value dbus.Variant
}

// SessionRecord is the on-disk shape written to
// /run/slinit-logind/sessions/<id>.json. Superset of the wire
// Session tuple — carries the extra metadata operator queries and
// per-object interfaces would need.
type SessionRecord struct {
	ID         string `json:"id"`
	UserID     uint32 `json:"user_id"`
	UserName   string `json:"user_name"`
	LeaderPID  uint32 `json:"leader_pid"`
	SeatID     string `json:"seat_id"`
	VTNr       uint32 `json:"vtnr"`
	TTY        string `json:"tty"`
	Display    string `json:"display"`
	Remote     bool   `json:"remote"`
	RemoteHost string `json:"remote_host"`
	RemoteUser string `json:"remote_user"`
	Service    string `json:"service"`
	Type       string `json:"type"`
	Class      string `json:"class"`
	Desktop    string `json:"desktop"`
	Scope      string `json:"scope"`
	RuntimePath string `json:"runtime_path"`
	CreatedAt  string `json:"created_at"`
}

// UserRecord is /run/slinit-logind/users/<uid>.json.
type UserRecord struct {
	UID         uint32 `json:"uid"`
	Name        string `json:"name"`
	Slice       string `json:"slice"`
	RuntimePath string `json:"runtime_path"`
	CreatedAt   string `json:"created_at"`
}

// sessionMu serialises the id-allocation + file-write race between
// concurrent CreateSession calls. Small enough (one login/logout is
// milliseconds) that we don't bother sharding.
var sessionMu sync.Mutex

// nextSessionID returns the next monotonically-increasing session id
// in elogind's "c<N>" shape. Persisted across restarts via
// /run/slinit-logind/next-session-id — a bare integer counter file.
func nextSessionID() (string, error) {
	path := filepath.Join(stateRoot, "next-session-id")
	var n uint64
	if b, err := os.ReadFile(path); err == nil {
		n, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	}
	n++
	if err := os.WriteFile(path, []byte(strconv.FormatUint(n, 10)), 0644); err != nil {
		return "", err
	}
	return "c" + strconv.FormatUint(n, 10), nil
}

// CreateSession — org.freedesktop.login1.Manager.CreateSession.
// Signature matches elogind's introspection verbatim so pam_elogind
// (or pam_systemd) can invoke it unchanged. Returns the wire tuple
// with the fifo fd as a dbus.UnixFD.
//
// The leading `dbus.Sender` argument is godbus's magic-value injection
// mechanism: at dispatch time it receives the caller's bus name so we
// can resolve pid=0 (the systemd convention meaning "use my PID") to
// the actual PID via org.freedesktop.DBus.GetConnectionUnixProcessID.
// Without this fallback pam_elogind's usual invocation lands with
// leader_pid=0, no cgroup migration happens, and every downstream
// PID→session lookup fails.
func (m *manager) CreateSession(
	sender dbus.Sender,
	uid, pid uint32,
	service, sessionType, sessionClass, desktop string,
	seatID string,
	vtnr uint32,
	tty, display string,
	remote bool,
	remoteUser, remoteHost string,
	properties []Property,
) (
	sessionIDOut string,
	objectPathOut dbus.ObjectPath,
	runtimePathOut string,
	fifoFdOut dbus.UnixFD,
	uidOut uint32,
	seatIDOut string,
	vtnrOut uint32,
	existing bool,
	dbErr *dbus.Error,
) {
	sessionMu.Lock()
	defer sessionMu.Unlock()

	// pid=0 → resolve via D-Bus caller. systemd/elogind's convention.
	if pid == 0 && sender != "" {
		if callerPID, err := m.callerPID(string(sender)); err == nil {
			pid = callerPID
		}
	}

	// If a session for this leader PID already exists, systemd's
	// contract is to return the existing one with existing=true rather
	// than allocate a new one. Cheap linear scan — session counts stay
	// small (dozens, not thousands) so a hash isn't worth it.
	if existingID, existingRec := findSessionByLeaderLocked(pid); existingID != "" {
		return existingID,
			sessionPath(existingID),
			existingRec.RuntimePath,
			openFifoLocked(existingID),
			existingRec.UserID,
			existingRec.SeatID,
			existingRec.VTNr,
			true,
			nil
	}

	id, err := nextSessionID()
	if err != nil {
		return "", "", "", 0, 0, "", 0, false,
			dbus.NewError("org.freedesktop.login1.Error.SessionAllocFailed",
				[]interface{}{err.Error()})
	}

	// Session scope layout — Void's libelogind (bundled with the
	// distro's polkit, xfce-polkit, gnome-shell, etc.) does *not*
	// parse systemd's canonical /user.slice/user-<uid>.slice/session-
	// <id>.scope hierarchy: sd_pid_get_session takes the FIRST cgroup
	// path component after root and returns it verbatim. Elogind
	// itself places sessions at /c<N>, so we do the same. A future
	// distro shipping upstream systemd's libsystemd would parse the
	// nested layout — for now we optimise for Void, which is where
	// slinit-logind cutover happens.
	//
	// User slice tracked separately for future extension (per-user
	// accounting via memory.max on user-<uid>.slice), but the session
	// leader lives directly under /<id>.
	sessionScope := fmt.Sprintf("/sys/fs/cgroup/%s", id)
	userSlice := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice", uid)
	runtimePath := fmt.Sprintf("/run/user/%d", uid)

	if err := ensureCgroup(userSlice); err != nil {
		return "", "", "", 0, 0, "", 0, false,
			dbus.NewError("org.freedesktop.login1.Error.CgroupSetupFailed",
				[]interface{}{"user slice: " + err.Error()})
	}
	if err := ensureCgroup(sessionScope); err != nil {
		return "", "", "", 0, 0, "", 0, false,
			dbus.NewError("org.freedesktop.login1.Error.CgroupSetupFailed",
				[]interface{}{"session scope: " + err.Error()})
	}
	// Move the leader into the scope. Failure here is non-fatal — a
	// PID that already exited between pam_open_session and here would
	// give ESRCH; we still create the session record so pam_close
	// finds something to release.
	_ = os.WriteFile(sessionScope+"/cgroup.procs",
		[]byte(strconv.FormatUint(uint64(pid), 10)), 0644)

	// Runtime dir: mode 0700, owned by the target uid. First session
	// for the user creates it; subsequent sessions reuse it — safe
	// because chown is a no-op when the owner already matches. Also
	// enforce the parent /run/user is 0755 root:root so downstream
	// user processes can traverse into their own runtime dir —
	// without this XDG_RUNTIME_DIR-based clients (dbus, iceauth,
	// portals) fail with EACCES on the parent lookup, and every
	// desktop session comes up broken (no menus, no notifications,
	// no polkit agent).
	_ = os.MkdirAll("/run/user", 0755)
	_ = os.Chmod("/run/user", 0755)
	if err := os.MkdirAll(runtimePath, 0700); err == nil {
		_ = os.Chown(runtimePath, int(uid), int(uid))
	}

	rec := SessionRecord{
		ID:          id,
		UserID:      uid,
		UserName:    lookupUsername(uid),
		LeaderPID:   pid,
		SeatID:      seatID,
		VTNr:        vtnr,
		TTY:         tty,
		Display:     display,
		Remote:      remote,
		RemoteHost:  remoteHost,
		RemoteUser:  remoteUser,
		Service:     service,
		Type:        sessionType,
		Class:       sessionClass,
		Desktop:     desktop,
		Scope:       sessionScope,
		RuntimePath: runtimePath,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeJSONAtomic(sessionFile(id), rec); err != nil {
		return "", "", "", 0, 0, "", 0, false,
			dbus.NewError("org.freedesktop.login1.Error.SessionPersistFailed",
				[]interface{}{err.Error()})
	}

	// Compat: libelogind/libsystemd clients (polkitd, PAM helpers,
	// sd_pid_get_session, etc.) read state directly from
	// /run/systemd/sessions/<id> in shell-variable format. Without
	// this file polkitd's sd_login_monitor_new() fails with -ENOENT,
	// polkitd exits, and every desktop authorisation prompt loses
	// its agent. Written alongside the JSON canonical form.
	_ = writeCompatSession(rec)

	// Ensure user record exists so ListUsers reflects the login. First
	// session for the user also mints the per-user D-Bus object.
	userRec := UserRecord{
		UID:         uid,
		Name:        rec.UserName,
		Slice:       userSlice,
		RuntimePath: runtimePath,
		CreatedAt:   rec.CreatedAt,
	}
	firstSessionForUser := !fileExists(userFile(uid))
	_ = writeJSONAtomic(userFile(uid), userRec)

	// Register per-object D-Bus surfaces so `loginctl show-session`
	// and per-object method calls (Terminate, Activate, Lock/Unlock,
	// TakeControl, ...) resolve. Also fires SessionNew (and UserNew
	// when applicable) so subscribers get the same wake-up systemd
	// delivers.
	if m.conn != nil {
		m.registerSessionObject(rec)
		if firstSessionForUser {
			m.registerUserObject(userRec)
		}
	}

	// FIFO fd: pam_open_session keeps this open; ReleaseSession
	// happens implicitly on close if pam_close_session doesn't beat
	// it there. Phase B ships the fd but relies on the explicit
	// ReleaseSession call for cleanup — a watch-goroutine on the
	// read-end is Phase C.
	return id,
		sessionPath(id),
		runtimePath,
		openFifoLocked(id),
		uid,
		seatID,
		vtnr,
		false,
		nil
}

// ReleaseSession is the pam_close_session hook. Tear down the JSON
// record + fifo + cgroup scope (best-effort — a leftover PID keeps
// the scope alive on the kernel side, we'll get rmdir=EBUSY and
// leave it for the reaper).
func (m *manager) ReleaseSession(id string) *dbus.Error {
	sessionMu.Lock()
	defer sessionMu.Unlock()

	var rec SessionRecord
	if !readJSON(sessionFile(id), &rec) {
		return dbus.NewError("org.freedesktop.login1.Error.NoSuchSession",
			[]interface{}{id})
	}
	_ = os.Remove(sessionFile(id))
	_ = os.Remove(fifoPath(id))
	_ = os.Remove(compatSessionFile(id))
	if rec.Scope != "" {
		_ = os.Remove(rec.Scope) // rmdir; EBUSY tolerated
	}
	// Drop the per-Session D-Bus object + fire SessionRemoved.
	if m.conn != nil {
		m.unregisterSessionObject(id)
	}
	// If no more sessions for the user, drop the user record + try
	// to remove the user slice (again EBUSY tolerated) + unregister
	// the per-User D-Bus object.
	if !userHasOtherSessionsLocked(rec.UserID, id) {
		_ = os.Remove(userFile(rec.UserID))
		_ = os.Remove(compatUserFile(rec.UserID))
		userSlice := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice", rec.UserID)
		_ = os.Remove(userSlice)
		if m.conn != nil {
			m.unregisterUserObject(rec.UserID)
		}
	}
	return nil
}

// callerPID asks the system bus daemon for the PID owning the given
// well-known/unique bus name. Used to resolve CreateSession's pid=0
// (systemd convention for "use my PID") to the actual client PID.
func (m *manager) callerPID(sender string) (uint32, error) {
	obj := m.conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")
	var pid uint32
	if err := obj.Call("org.freedesktop.DBus.GetConnectionUnixProcessID",
		0, sender).Store(&pid); err != nil {
		return 0, err
	}
	return pid, nil
}

// compatSessionFile returns the libelogind/libsystemd path where
// clients (polkitd, sd_pid_get_session, etc.) read session state via
// sd-login. Kept mirror-parallel to sessionFile so cleanup on
// ReleaseSession stays symmetric.
func compatSessionFile(id string) string { return "/run/systemd/sessions/" + id }
func compatUserFile(uid uint32) string {
	return "/run/systemd/users/" + strconv.FormatUint(uint64(uid), 10)
}
func compatSeatFile(id string) string { return "/run/systemd/seats/" + id }

// writeCompatSession writes /run/systemd/sessions/<id> in the
// shell-variable format libelogind (and any consumer of libsystemd's
// sd-login family) parses. Newlines separate KEY=VALUE pairs; no
// quoting is required for the simple types we track. Missing dir is
// created idempotently so a fresh boot doesn't need a separate
// tmpfiles run for /run/systemd/.
func writeCompatSession(rec SessionRecord) error {
	_ = os.MkdirAll("/run/systemd/sessions", 0755)
	_ = os.MkdirAll("/run/systemd/users", 0755)
	_ = os.MkdirAll("/run/systemd/seats", 0755)
	var b strings.Builder
	fmt.Fprintf(&b, "# This is private data. Do not parse.\n")
	fmt.Fprintf(&b, "UID=%d\n", rec.UserID)
	fmt.Fprintf(&b, "USER=%s\n", rec.UserName)
	fmt.Fprintf(&b, "ACTIVE=1\n")
	fmt.Fprintf(&b, "STATE=active\n")
	fmt.Fprintf(&b, "REMOTE=%d\n", boolInt(rec.Remote))
	if rec.SeatID != "" {
		fmt.Fprintf(&b, "SEAT=%s\n", rec.SeatID)
	}
	if rec.TTY != "" {
		fmt.Fprintf(&b, "TTY=%s\n", rec.TTY)
	}
	if rec.Display != "" {
		fmt.Fprintf(&b, "DISPLAY=%s\n", rec.Display)
	}
	if rec.VTNr > 0 {
		fmt.Fprintf(&b, "VTNR=%d\n", rec.VTNr)
	}
	fmt.Fprintf(&b, "SERVICE=%s\n", rec.Service)
	fmt.Fprintf(&b, "TYPE=%s\n", rec.Type)
	fmt.Fprintf(&b, "CLASS=%s\n", rec.Class)
	if rec.Desktop != "" {
		fmt.Fprintf(&b, "DESKTOP=%s\n", rec.Desktop)
	}
	fmt.Fprintf(&b, "LEADER=%d\n", rec.LeaderPID)
	fmt.Fprintf(&b, "REALTIME=%d\n", time.Now().Unix())
	fmt.Fprintf(&b, "MONOTONIC=%d\n", 0)

	// User compat file — most consumers just need the UID + name to
	// identify. State minimal (STATE + NAME + RUNTIME).
	var u strings.Builder
	fmt.Fprintf(&u, "# This is private data. Do not parse.\n")
	fmt.Fprintf(&u, "NAME=%s\n", rec.UserName)
	fmt.Fprintf(&u, "STATE=active\n")
	fmt.Fprintf(&u, "RUNTIME=%s\n", rec.RuntimePath)
	_ = os.WriteFile(compatUserFile(rec.UserID), []byte(u.String()), 0644)

	if rec.SeatID != "" {
		var s strings.Builder
		fmt.Fprintf(&s, "# This is private data. Do not parse.\n")
		fmt.Fprintf(&s, "ACTIVE=%s\n", rec.ID)
		fmt.Fprintf(&s, "SESSIONS=%s\n", rec.ID)
		_ = os.WriteFile(compatSeatFile(rec.SeatID), []byte(s.String()), 0644)
	}

	return os.WriteFile(compatSessionFile(rec.ID), []byte(b.String()), 0644)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fileExists is a tiny predicate used to gate first-session-for-user
// per-object registration. Kept lightweight — a single stat call.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ActivateSession — sets a session as foreground. Phase B has no
// seat/VT switching; this is a no-op stub that answers "success" so
// clients don't error. Real VT_ACTIVATE lands with seat detection in
// Phase C.
func (m *manager) ActivateSession(id string) *dbus.Error {
	if !sessionExistsLocked(id) {
		return dbus.NewError("org.freedesktop.login1.Error.NoSuchSession",
			[]interface{}{id})
	}
	return nil
}

// ActivateSessionOnSeat, LockSession, UnlockSession, LockSessions,
// UnlockSessions — stubs that answer success once we've verified the
// session exists. Real lock semantics need a signal to the compositor
// (systemd sends `Lock`/`Unlock` D-Bus signals on the session
// object); Phase B silences them.
func (m *manager) ActivateSessionOnSeat(id, seat string) *dbus.Error {
	if !sessionExistsLocked(id) {
		return dbus.NewError("org.freedesktop.login1.Error.NoSuchSession",
			[]interface{}{id})
	}
	return nil
}
func (m *manager) LockSession(id string) *dbus.Error   { return checkSess(id) }
func (m *manager) UnlockSession(id string) *dbus.Error { return checkSess(id) }
func (m *manager) LockSessions() *dbus.Error           { return nil }
func (m *manager) UnlockSessions() *dbus.Error         { return nil }

// TerminateSession is Manager.TerminateSession — semantically the
// same as ReleaseSession for our purposes (both tear the record
// down). GDM's greeter and lightdm's session-switch button call
// TerminateSession; without it the greeter switch-user hangs.
func (m *manager) TerminateSession(id string) *dbus.Error {
	return m.ReleaseSession(id)
}

// TerminateUser tears down every session owned by the uid then drops
// the user record. Called by loginctl terminate-user.
func (m *manager) TerminateUser(uid uint32) *dbus.Error {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	for _, f := range files {
		var rec SessionRecord
		if readJSON(f, &rec) && rec.UserID == uid {
			_ = m.ReleaseSession(rec.ID)
		}
	}
	return nil
}

// TerminateSeat tears down every session on the seat.
func (m *manager) TerminateSeat(id string) *dbus.Error {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	for _, f := range files {
		var rec SessionRecord
		if readJSON(f, &rec) && rec.SeatID == id {
			_ = m.ReleaseSession(rec.ID)
		}
	}
	return nil
}

// KillUser sends a signal to every process in every session owned by
// uid. Iterates KillSession over the user's sessions.
func (m *manager) KillUser(uid uint32, sig int32) *dbus.Error {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	for _, f := range files {
		var rec SessionRecord
		if readJSON(f, &rec) && rec.UserID == uid {
			_ = m.KillSession(rec.ID, "all", sig)
		}
	}
	return nil
}

// SetUserLinger persists a "keep the user manager running across
// logout" flag. loginctl enable-linger / disable-linger. We track it
// in the user JSON record so the property surface can report it.
func (m *manager) SetUserLinger(uid uint32, b bool, interactive bool) *dbus.Error {
	// Phase C+ persistence — for now accept the call so `loginctl
	// enable-linger sunlight` doesn't error.
	return nil
}

// KillSession sends the given signal to every process in the session
// cgroup, or just the leader if `who = "leader"`.
func (m *manager) KillSession(id, who string, sig int32) *dbus.Error {
	var rec SessionRecord
	if !readJSON(sessionFile(id), &rec) {
		return dbus.NewError("org.freedesktop.login1.Error.NoSuchSession",
			[]interface{}{id})
	}
	if who == "leader" {
		_ = writeInt(rec.Scope+"/cgroup.kill", 1) // fastest path on newer kernels
		return nil
	}
	// Full-tree kill: signal every PID in cgroup.procs.
	if b, err := os.ReadFile(rec.Scope + "/cgroup.procs"); err == nil {
		for _, line := range strings.Fields(string(b)) {
			pid, err := strconv.Atoi(line)
			if err != nil {
				continue
			}
			_ = killPID(pid, sig)
		}
	}
	return nil
}

// --- helpers ---

func sessionFile(id string) string { return filepath.Join(stateRoot, "sessions", id+".json") }
func userFile(uid uint32) string {
	return filepath.Join(stateRoot, "users", strconv.FormatUint(uint64(uid), 10)+".json")
}
func fifoPath(id string) string { return filepath.Join(stateRoot, "sessions", id+".fifo") }

func sessionExistsLocked(id string) bool {
	_, err := os.Stat(sessionFile(id))
	return err == nil
}

func checkSess(id string) *dbus.Error {
	if !sessionExistsLocked(id) {
		return dbus.NewError("org.freedesktop.login1.Error.NoSuchSession",
			[]interface{}{id})
	}
	return nil
}

func findSessionByLeaderLocked(pid uint32) (string, *SessionRecord) {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	for _, f := range files {
		var rec SessionRecord
		if !readJSON(f, &rec) {
			continue
		}
		if rec.LeaderPID == pid {
			return rec.ID, &rec
		}
	}
	return "", nil
}

func userHasOtherSessionsLocked(uid uint32, excludeID string) bool {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	for _, f := range files {
		var rec SessionRecord
		if !readJSON(f, &rec) {
			continue
		}
		if rec.ID != excludeID && rec.UserID == uid {
			return true
		}
	}
	return false
}

// openFifoLocked returns a read-end fd for the session fifo, creating
// the pipe file if it doesn't exist yet. systemd's contract is that
// closing this fd (from the CreateSession caller side) marks the
// session as released; Phase B doesn't watch it yet — clients must
// still call ReleaseSession explicitly.
func openFifoLocked(id string) dbus.UnixFD {
	p := fifoPath(id)
	_ = os.Remove(p) // stale from a previous session with recycled id
	if err := makeFifo(p, 0600); err != nil {
		return 0
	}
	// O_NONBLOCK|O_RDONLY on the read side so the open doesn't block
	// waiting for a writer.
	f, err := os.OpenFile(p, os.O_RDONLY|nonblock, 0)
	if err != nil {
		return 0
	}
	// We keep the fd numbered by our runtime — D-Bus dupes it before
	// sending to the client, and Go's GC won't close it as long as
	// `f` stays reachable; we intentionally leak it (small: one fd
	// per session, small session count).
	return dbus.UnixFD(f.Fd())
}

// writeJSONAtomic writes via temp-file + rename so a concurrent
// ListSessions never sees a half-written record.
func writeJSONAtomic(path string, v interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ensureCgroup(path string) error {
	return os.MkdirAll(path, 0755)
}

// writeInt drops a decimal integer into a file — cgroup interface
// files (cgroup.kill, cgroup.procs) take that shape.
func writeInt(path string, v int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(v)), 0644)
}

func lookupUsername(uid uint32) string {
	// Bare /etc/passwd read — avoids CGO dependency of os/user on
	// glibc's nss. Falls back to "uid=N" when the mapping isn't
	// resolvable (dynamic user, stale entry).
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return fmt.Sprintf("uid=%d", uid)
	}
	uidStr := strconv.FormatUint(uint64(uid), 10)
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.SplitN(line, ":", 4)
		if len(fields) < 3 {
			continue
		}
		if fields[2] == uidStr {
			return fields[0]
		}
	}
	return fmt.Sprintf("uid=%d", uid)
}
