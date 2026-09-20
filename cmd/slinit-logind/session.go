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
	"sort"
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
	// Move the caller's ancestor chain into the scope. The caller
	// that reached CreateSession is usually a short-lived PAM helper
	// (pam_elogind's account-helper or gdm's sudo-style wrapper) that
	// exits between pam_open_session's D-Bus call and our WriteFile —
	// leaving the write silent-successful (the kernel treats a
	// dead-pid write as a no-op) but the scope empty. That
	// desynchronises the session tree: gdm-session-worker,
	// gdm-wayland-session, gnome-session-binary and gnome-shell get
	// forked from the same PAM ancestry but stay in the root cgroup,
	// so `sd_pid_get_session()` on any of them returns nothing.
	// gnome-shell then dies with "Failed to setup: Failed to find any
	// matching session" and gnome-session escalates that to
	// "Unrecoverable failure in required component org.gnome.Shell".
	//
	// Walk up /proc/<pid>/status:PPid greedily — read the ancestor
	// chain BEFORE it can die — and migrate the first live ancestor
	// we can confirm (via cgroup.procs re-read). For the PAM chain
	//   gdm-session-worker → sh → pam_elogind_helper → us
	// the first stable ancestor is normally gdm-session-worker, which
	// stays alive for the whole session and forks every user-facing
	// process we want in the scope.
	ancestors := []uint32{pid}
	{
		cur := pid
		for i := 0; i < 8; i++ {
			ppid, ok := readPPid(cur)
			if !ok || ppid <= 1 || ppid == cur {
				break
			}
			ancestors = append(ancestors, ppid)
			cur = ppid
		}
	}
	if m.debug {
		chain := make([]string, 0, len(ancestors))
		for _, a := range ancestors {
			chain = append(chain, fmt.Sprintf("%d(%s)", a, procComm(a)))
		}
		m.dbgf("CreateSession id=%s uid=%d leader=%d service=%q type=%q class=%q seat=%q vtnr=%d chain=%s",
			id, uid, pid, service, sessionType, sessionClass, seatID, vtnr,
			strings.Join(chain, " <- "))
	}
	// Migrate every live ancestor in the chain plus every
	// gdm-session-worker whose real uid matches this session's uid.
	//
	// The dbus caller is typically a transient PAM helper that exits
	// inside microseconds of our WriteFile — kernel accepts the
	// write, then removes the pid on process exit, leaving the scope
	// empty just after we walk away. On Void's gdm layout the PAM
	// helper's parent chain runs through /usr/bin/gdm itself, NOT
	// through gdm-session-worker, so pure ancestor-walking never
	// touches the process that actually forks the user-facing
	// binaries (gdm-wayland-session → dbus-run-session →
	// gnome-session-binary → gnome-shell). Migrating those
	// gdm-session-worker processes explicitly by name gives the
	// scope the long-lived root the downstream fork tree needs;
	// kernel semantics carry the scope forward across every
	// subsequent fork so `sd_pid_get_session()` reads back the
	// session id on gnome-shell.
	moved := 0
	writeProc := func(candPID uint32, why string) {
		werr := os.WriteFile(sessionScope+"/cgroup.procs",
			[]byte(strconv.FormatUint(uint64(candPID), 10)), 0644)
		if werr == nil {
			moved++
		}
		// A successful write proves nothing — the kernel accepts
		// cgroup.procs writes for a pid that has already exited, and
		// the migration we care about is the one that survives long
		// enough to fork the session tree. Read the pid's own cgroup
		// back so the log records where it actually landed.
		m.dbgf("cgroup %s <- pid=%d (%s, comm=%q) write=%v now=%s",
			id, candPID, why, procComm(candPID), werr, procCgroup(candPID))
	}
	for _, cand := range ancestors {
		writeProc(cand, "ancestor")
	}
	// Snapshot /proc: any gdm-session-worker (or gnome-session
	// helper) belonging to this uid is a session-defining pivot.
	// We check comm rather than the full argv to keep the scan
	// cheap — /proc/<pid>/comm is a single 16-byte read.
	if entries, err := os.ReadDir("/proc"); err == nil {
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			candPID, err := strconv.ParseUint(ent.Name(), 10, 32)
			if err != nil {
				continue
			}
			commBytes, err := os.ReadFile("/proc/" + ent.Name() + "/comm")
			if err != nil {
				continue
			}
			comm := strings.TrimSpace(string(commBytes))
			// Match gdm-session-worker's short name. Kernel
			// truncates comm to 15 chars; the process shows up as
			// "gdm-session-wor" — startsWith covers both forms. We
			// don't uid-filter: gdm-session-worker holds root
			// throughout PAM setup and only switches to the target
			// uid inside the exec'd session command, so a loginuid
			// or ruid match would false-negative every fresh
			// session at exactly the moment we need to migrate.
			// The name filter is scope enough — gdm-session-worker
			// only exists in the PAM-active gdm chain.
			if !strings.HasPrefix(comm, "gdm-session-wor") {
				continue
			}
			writeProc(uint32(candPID), "gdm-worker-scan")
		}
	}
	if moved == 0 {
		fmt.Fprintf(os.Stderr, "slinit-logind: cgroup.procs %s: no ancestor migrated for pid=%d (chain: %v)\n",
			sessionScope, pid, ancestors)
	}
	// Final state of the scope, so the log says who is actually in it
	// once every write has been attempted.
	if procs, rerr := os.ReadFile(sessionScope + "/cgroup.procs"); rerr == nil {
		m.dbgf("cgroup %s settled: procs=[%s]", id,
			strings.Join(strings.Fields(string(procs)), " "))
	}

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
	// The user and seat records list sessions, so removing one has to
	// rewrite them — otherwise sd_uid_get_sessions() keeps handing out
	// a session id whose files are already gone.
	rewriteCompatAggregates()
	return nil
}

// readPPid parses PPid from /proc/<pid>/status. Returns (ppid, true)
// on success; (0, false) if the process is gone or the field is
// missing. Used by CreateSession's cgroup-migration fallback.
func readPPid(pid uint32) (uint32, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "PPid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		n, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil {
			return 0, false
		}
		return uint32(n), true
	}
	return 0, false
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

	if err := os.WriteFile(compatSessionFile(rec.ID), []byte(b.String()), 0644); err != nil {
		return err
	}
	// The per-user and per-seat records are aggregates over every live
	// session, so they can't be derived from the one being created.
	rewriteCompatAggregates()
	return nil
}

// loadSessionRecords reads every live session record off disk. The
// aggregates below need the whole set; there's no in-memory session
// table to consult (state lives in /run so it survives a daemon
// restart).
func loadSessionRecords() []SessionRecord {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	sort.Strings(files)
	out := make([]SessionRecord, 0, len(files))
	for _, f := range files {
		var rec SessionRecord
		if readJSON(f, &rec) {
			out = append(out, rec)
		}
	}
	return out
}

// rewriteCompatAggregates rebuilds /run/systemd/users/<uid> and
// /run/systemd/seats/<id> from the full set of live sessions.
//
// These files are not decoration. sd_uid_get_sessions() — which is how
// mutter finds its session when sd_pid_get_session() comes up empty —
// reads nothing but the SESSIONS / ACTIVE_SESSIONS / ONLINE_SESSIONS
// arrays out of the user file. An earlier revision wrote only
// NAME/STATE/RUNTIME there, so the lookup returned zero sessions and
// gnome-shell died with "Failed to find any matching session" even
// though the session existed and the leader was correctly placed in
// its cgroup.
//
// The seat file has the mirror-image hazard: writing it per-session
// clobbered the CAN_TTY / CAN_GRAPHICAL / IS_SEAT0 flags that
// ensureSeat had put there at startup, so sd_seat_can_graphical()
// started answering "no" the moment the first session was created.
// Everything is rewritten together here, from one source of truth.
//
// We report every session as active: without VT-activity tracking
// there is no basis to call one session foreground and another not,
// and the per-session records already say ACTIVE=1.
func rewriteCompatAggregates() {
	_ = os.MkdirAll("/run/systemd/users", 0755)
	_ = os.MkdirAll("/run/systemd/seats", 0755)

	sessions := loadSessionRecords()

	byUID := map[uint32][]SessionRecord{}
	bySeat := map[string][]SessionRecord{}
	for _, rec := range sessions {
		byUID[rec.UserID] = append(byUID[rec.UserID], rec)
		if rec.SeatID != "" {
			bySeat[rec.SeatID] = append(bySeat[rec.SeatID], rec)
		}
	}

	for uid, recs := range byUID {
		var ids, seats []string
		display := ""
		for _, r := range recs {
			ids = append(ids, r.ID)
			if r.SeatID != "" {
				seats = append(seats, r.SeatID)
			}
			// sd_uid_get_display() wants the user's graphical
			// session; first wayland/x11 one wins, as in elogind.
			if display == "" && (r.Type == "wayland" || r.Type == "x11") {
				display = r.ID
			}
		}
		seats = uniqueStrings(seats)

		var u strings.Builder
		fmt.Fprintf(&u, "# This is private data. Do not parse.\n")
		fmt.Fprintf(&u, "NAME=%s\n", recs[0].UserName)
		fmt.Fprintf(&u, "STATE=active\n")
		fmt.Fprintf(&u, "STOPPING=no\n")
		fmt.Fprintf(&u, "RUNTIME=%s\n", recs[0].RuntimePath)
		if display != "" {
			fmt.Fprintf(&u, "DISPLAY=%s\n", display)
		}
		fmt.Fprintf(&u, "SESSIONS=%s\n", strings.Join(ids, " "))
		fmt.Fprintf(&u, "ACTIVE_SESSIONS=%s\n", strings.Join(ids, " "))
		fmt.Fprintf(&u, "ONLINE_SESSIONS=%s\n", strings.Join(ids, " "))
		fmt.Fprintf(&u, "SEATS=%s\n", strings.Join(seats, " "))
		fmt.Fprintf(&u, "ACTIVE_SEATS=%s\n", strings.Join(seats, " "))
		fmt.Fprintf(&u, "ONLINE_SEATS=%s\n", strings.Join(seats, " "))
		_ = os.WriteFile(compatUserFile(uid), []byte(u.String()), 0644)
	}

	// Every registered seat, not just the ones with sessions — a seat
	// with no session still has to advertise its capabilities or gdm
	// won't spawn a greeter on it.
	seatFiles, _ := filepath.Glob(filepath.Join(stateRoot, "seats", "*.json"))
	for _, f := range seatFiles {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		writeCompatSeat(id, bySeat[id])
	}
}

// writeCompatSeat renders /run/systemd/seats/<id>. Field set and order
// follow elogind's seat_save().
func writeCompatSeat(id string, sessions []SessionRecord) {
	var s strings.Builder
	fmt.Fprintf(&s, "# This is private data. Do not parse.\n")
	fmt.Fprintf(&s, "IS_SEAT0=%d\n", boolInt(id == "seat0"))
	fmt.Fprintf(&s, "CAN_MULTI_SESSION=1\n")
	fmt.Fprintf(&s, "CAN_TTY=1\n")
	fmt.Fprintf(&s, "CAN_GRAPHICAL=1\n")
	if len(sessions) > 0 {
		// Newest session is the foreground one — sessions arrive in
		// id order from loadSessionRecords.
		active := sessions[len(sessions)-1]
		fmt.Fprintf(&s, "ACTIVE=%s\n", active.ID)
		fmt.Fprintf(&s, "ACTIVE_UID=%d\n", active.UserID)

		ids := make([]string, 0, len(sessions))
		uids := make([]string, 0, len(sessions))
		for _, r := range sessions {
			ids = append(ids, r.ID)
			uids = append(uids, strconv.FormatUint(uint64(r.UserID), 10))
		}
		fmt.Fprintf(&s, "SESSIONS=%s\n", strings.Join(ids, " "))
		fmt.Fprintf(&s, "UIDS=%s\n", strings.Join(uids, " "))
	}
	_ = os.WriteFile(compatSeatFile(id), []byte(s.String()), 0644)
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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
