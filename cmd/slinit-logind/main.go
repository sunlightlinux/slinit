// slinit-logind — native org.freedesktop.login1 D-Bus service for
// Sunlight OS, replacing elogind incrementally.
//
// Phase A (this file): scaffold + read-only Manager interface + power
// method proxies. Session/User/Seat state is read from
// /run/slinit-logind/{sessions,users,seats}/*.json — populated in
// Phase B by pam_slinit.so at login.
//
// Method inventory shipped in this cut (org.freedesktop.login1.Manager):
//   ListSessions, ListUsers, ListSeats, ListInhibitors — read-only
//   GetSession, GetUser, GetSeat                       — lookup by id
//   PowerOff, Reboot, Halt, Suspend, Hibernate,
//     HybridSleep, SuspendThenHibernate               — power ops
//   CanPowerOff, CanReboot, CanHalt, CanSuspend,
//     CanHibernate, CanHybridSleep,
//     CanSuspendThenHibernate                          — capability probes
//   Inhibit                                            — stub returns
//                                                       a pipe fd; real
//                                                       enforcement lands
//                                                       with the inhibitor
//                                                       registry in Phase C
//
// Not yet: signals (SessionNew/Removed, PrepareForShutdown),
// per-Session/User/Seat object paths, and the CreateSession /
// ReleaseSession / ActivateSession methods PAM invokes. Those land
// alongside the pam_slinit.so module.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

const (
	busName   = "org.freedesktop.login1"
	objPath   = "/org/freedesktop/login1"
	iface     = "org.freedesktop.login1.Manager"
	stateRoot = "/run/slinit-logind"
)

var (
	// slinitctlPath is where power methods look for the shutdown CLI.
	// Overridable so a nested test build can point at a hermetic
	// binary; unset in production for exec.LookPath discovery.
	slinitctlPath = ""
)

// Session, User, Seat mirror the D-Bus struct shape systemd's login1
// ListSessions returns. Only the fields that appear in the wire
// tuple; extra metadata lives in the JSON file on disk.
//
// JSON tags match SessionRecord/UserRecord field names so reading a
// persisted record straight into a wire-shaped struct DTRT — without
// them Go's decoder silently drops every unrecognised snake_case key
// and the wire tuple ships with zeros for uid/name/seat.
type Session struct {
	ID       string          `json:"id"`
	UserID   uint32          `json:"user_id"`
	UserName string          `json:"user_name"`
	SeatID   string          `json:"seat_id"`
	Path     dbus.ObjectPath `json:"-"`
}

type User struct {
	UID  uint32          `json:"uid"`
	Name string          `json:"name"`
	Path dbus.ObjectPath `json:"-"`
}

type Seat struct {
	ID   string          `json:"id"`
	Path dbus.ObjectPath `json:"-"`
}

// Inhibitor rows are read from /run/slinit-logind/inhibitors/*.json.
// Phase A ships an empty registry — the Inhibit() method returns a
// pipe fd but doesn't enforce anything yet.
type Inhibitor struct {
	What  string
	Who   string
	Why   string
	Mode  string
	UID   uint32
	PID   uint32
}

// manager holds the mutable state visible over D-Bus. Locked as a
// unit because read methods (ListSessions/Users/Seats) fan out and
// the state files can turn over under a live PAM session.
//
// conn is set from main() after ConnectSystemBus so per-Session /
// User / Seat objects can be registered when CreateSession fires
// (see objects.go). Kept on the manager rather than a package global
// so a test using two separate manager instances against a synthetic
// bus doesn't cross-talk on the exported paths.
type manager struct {
	mu   sync.RWMutex
	conn *dbus.Conn

	// Manager.WallMessage / EnableWallMessages are writable properties
	// (see managerprops.go). We store what a client sets so the value
	// round-trips through `loginctl` the way it does under elogind;
	// nothing consumes it until ScheduleShutdown lands and has a wall
	// to broadcast.
	enableWallMessages bool
	wallMessage        string

	// sessionProps holds each exported session's property table so a
	// later SetLockedHint / SetIdleHint can write through it and emit
	// PropertiesChanged. Guarded by mu like the rest of the struct.
	sessionProps map[string]*prop.Properties

	// seatProps is the same for seats: ActiveSession and Sessions move
	// with every session change and VT switch (see vt.go).
	seatProps map[string]*prop.Properties

	// debug mirrors the --debug flag. Session setup is the one place
	// where a post-mortem is useless: the gdm/gnome fork chain we are
	// trying to place in a cgroup is gone within a second of the
	// failure, so the trace has to be written as it happens.
	debug bool
}

// dbgf logs one line to stderr when --debug is set. main() points
// os.Stderr at /var/log/slinit-logind.log in that mode, since slinit's
// runner attaches fd 2 to /dev/null.
func (m *manager) dbgf(format string, args ...any) {
	if !m.debug {
		return
	}
	fmt.Fprintf(os.Stderr, "slinit-logind: "+format+"\n", args...)
}

// procComm reads /proc/<pid>/comm, for log lines that need to name a
// process rather than just number it. Returns "?" when the process is
// already gone — which is itself the interesting case here.
func procComm(pid uint32) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return "?"
	}
	return string(bytesTrimSpace(b))
}

// procCgroup reads the unified (0::) cgroup path of a pid. "?" when the
// process is gone, "" when it has no unified line.
func procCgroup(pid uint32) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "?"
	}
	for _, line := range splitLines(string(b)) {
		if hasPrefix(line, "0::") {
			return line[3:]
		}
	}
	return ""
}

func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\n') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\n') {
		end--
	}
	return b[start:end]
}

// ListSessions returns [(id, uid, user, seat, path)]. On a fresh
// system with no PAM integration yet this is empty; once
// pam_slinit.so lands and writes /run/slinit-logind/sessions/*.json
// each login shows up here.
func (m *manager) ListSessions() ([]Session, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Session{}
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	sort.Strings(files)
	for _, f := range files {
		var s Session
		if readJSON(f, &s) {
			s.Path = sessionPath(s.ID)
			out = append(out, s)
		}
	}
	return out, nil
}

// ListUsers returns [(uid, name, path)].
func (m *manager) ListUsers() ([]User, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []User{}
	files, _ := filepath.Glob(filepath.Join(stateRoot, "users", "*.json"))
	sort.Strings(files)
	for _, f := range files {
		var u User
		if readJSON(f, &u) {
			u.Path = userPath(u.UID)
			out = append(out, u)
		}
	}
	return out, nil
}

// ListSeats returns [(id, path)]. seat0 is always present as the
// primary seat once seat detection lands in Phase C; today we render
// whatever /run/slinit-logind/seats/ contains, which is empty by
// default.
func (m *manager) ListSeats() ([]Seat, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Seat{}
	files, _ := filepath.Glob(filepath.Join(stateRoot, "seats", "*.json"))
	sort.Strings(files)
	for _, f := range files {
		var s Seat
		if readJSON(f, &s) {
			s.Path = seatPath(s.ID)
			out = append(out, s)
		}
	}
	return out, nil
}

// ListInhibitors returns [(what, who, why, mode, uid, pid)]. Phase A
// ships an empty registry — the Inhibit() method below hands out fds
// but doesn't record them yet.
func (m *manager) ListInhibitors() ([]Inhibitor, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return []Inhibitor{}, nil
}

// GetSession / GetUser / GetSeat resolve by id → object path so
// callers can talk to per-object interfaces.
//
// "self" and "auto" are the caller-relative names systemd defines:
// loginctl passes "auto" whenever it is asked about the current
// session, so without them `loginctl session-status` with no argument
// fails outright.
func (m *manager) GetSession(sender dbus.Sender, id string) (dbus.ObjectPath, *dbus.Error) {
	if id == "self" || id == "auto" {
		rec, ok := m.resolveAlias(sender, id)
		if !ok {
			return "/", dbus.NewError("org.freedesktop.login1.Error.NoSessionForPID",
				[]interface{}{"caller is not part of any session"})
		}
		return sessionPath(rec.ID), nil
	}
	if !sessionExistsLocked(id) {
		return "/", dbus.NewError("org.freedesktop.login1.Error.NoSuchSession",
			[]interface{}{id})
	}
	return sessionPath(id), nil
}
func (m *manager) GetUser(uid uint32) (dbus.ObjectPath, *dbus.Error) {
	if _, err := os.Stat(userFile(uid)); err != nil {
		return "/", dbus.NewError("org.freedesktop.login1.Error.NoSuchUser",
			[]interface{}{uid})
	}
	return userPath(uid), nil
}
func (m *manager) GetSeat(id string) (dbus.ObjectPath, *dbus.Error) {
	return seatPath(id), nil
}

// GetSessionByPID looks up which session a given process belongs to.
// XFCE session integration and many desktop apps call this at startup
// to discover their own session's object path. We walk sessions/*.json
// and match on leader_pid; when the querying process is a descendant
// of a session's leader we should still find it by /proc/PID/cgroup
// reading — that fallback lands in Phase C+.
//
// pid 0 means "the caller", as everywhere else in this API — elogind
// resolves it from the bus message's credentials. Without that a client
// asking about itself the short way gets NoSessionForPID for /proc/0.
func (m *manager) GetSessionByPID(sender dbus.Sender, pid uint32) (dbus.ObjectPath, *dbus.Error) {
	if pid == 0 {
		callerPID, err := m.callerPID(string(sender))
		if err != nil {
			return "/", dbus.NewError("org.freedesktop.login1.Error.NoSessionForPID",
				[]interface{}{"cannot resolve caller pid: " + err.Error()})
		}
		pid = callerPID
	}
	if id, _ := findSessionByLeaderLocked(pid); id != "" {
		return sessionPath(id), nil
	}
	// Fallback: derive session via /proc/PID/cgroup — the process may
	// have been forked from the session leader without being the leader
	// itself, but it inherits the session-<id>.scope cgroup.
	if id := findSessionByCgroup(pid); id != "" {
		return sessionPath(id), nil
	}
	return "/", dbus.NewError("org.freedesktop.login1.Error.NoSessionForPID",
		[]interface{}{pid})
}

// GetUserByPID resolves a PID → owning user's object path. XFCE
// applications call this when talking to xdg-desktop-portal etc.
func (m *manager) GetUserByPID(sender dbus.Sender, pid uint32) (dbus.ObjectPath, *dbus.Error) {
	if pid == 0 {
		callerPID, err := m.callerPID(string(sender))
		if err != nil {
			return "/", dbus.NewError("org.freedesktop.login1.Error.NoUserForPID",
				[]interface{}{"cannot resolve caller pid: " + err.Error()})
		}
		pid = callerPID
	}
	if id, rec := findSessionByLeaderLocked(pid); id != "" {
		return userPath(rec.UserID), nil
	}
	if id := findSessionByCgroup(pid); id != "" {
		var rec SessionRecord
		if readJSON(sessionFile(id), &rec) {
			return userPath(rec.UserID), nil
		}
	}
	// Fallback: read /proc/PID/status Uid: line.
	if uid, ok := procUID(pid); ok {
		if _, err := os.Stat(userFile(uid)); err == nil {
			return userPath(uid), nil
		}
	}
	return "/", dbus.NewError("org.freedesktop.login1.Error.NoSessionForPID",
		[]interface{}{pid})
}

// findSessionByCgroup extracts the session id from /proc/PID/cgroup.
// Empty string when the pid isn't in a session scope.
//
// We place sessions flat, at /sys/fs/cgroup/<id>, which is elogind's
// layout — so the session id is simply the FIRST path component of the
// unified (0::) line. That is exactly what libelogind's
// cg_path_get_session() reads, and matching it is the whole reason for
// the flat layout (see CreateSession).
//
// This used to scan for systemd's nested `session-<id>.scope` naming
// instead, which we never write. The result was that the lookup only
// ever succeeded for a session's own leader — the one pid
// findSessionByLeaderLocked already covers — and failed for every
// process forked from it. GetSessionByPID on a desktop's gnome-shell
// answered NoSessionForPID while /proc/<pid>/cgroup plainly read
// `0::/c2`.
//
// The nested form is still accepted afterwards: it costs a few lines,
// and a session record written by an older slinit-logind (or a host
// that really does use systemd's layout) stays resolvable.
func findSessionByCgroup(pid uint32) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	return sessionFromCgroup(string(b), func(id string) bool {
		return fileExists(sessionFile(id))
	})
}

// sessionFromCgroup is the parsing half of findSessionByCgroup, split
// out so it can be tested against content we choose rather than
// against whatever cgroup the test runner happens to live in.
//
// exists reports whether a session record is on disk for an id. Both
// layouts below consult it, because a path that merely looks like a
// session is not one: on a systemd host every process sits under
// .../session-<id>.scope, so a parser that trusts the name alone
// resolves any pid at all to an id this daemon has never heard of.
func sessionFromCgroup(content string, exists func(string) bool) string {
	for _, line := range splitLines(content) {
		if !hasPrefix(line, "0::") {
			continue
		}
		p := line[3:]
		if !hasPrefix(p, "/") {
			continue
		}
		// First path component, flat elogind layout.
		first := p[1:]
		if i := indexByte(first, '/'); i >= 0 {
			first = first[:i]
		}
		if first != "" && exists(first) {
			return first
		}
		// systemd's nested naming: .../session-<id>.scope
		for i := 0; i+len("session-") < len(p); i++ {
			if p[i:i+len("session-")] != "session-" {
				continue
			}
			end := i + len("session-")
			for end < len(p) && p[end] != '.' && p[end] != '/' {
				end++
			}
			if id := p[i+len("session-") : end]; exists(id) {
				return id
			}
			break
		}
	}
	return ""
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// procUID returns the real UID of a running process from
// /proc/PID/status. Bool is false when the process is gone or
// /proc isn't accessible.
func procUID(pid uint32) (uint32, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range splitLines(string(b)) {
		if !hasPrefix(line, "Uid:") {
			continue
		}
		var ruid uint32
		if _, err := fmt.Sscanf(line, "Uid:\t%d", &ruid); err == nil {
			return ruid, true
		}
	}
	return 0, false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// Power methods proxy to slinit-shutdown. Interactive is systemd's
// "ask polkit" flag; we ignore it for now (the D-Bus policy file
// gates who can call these). Errors are surfaced as
// org.freedesktop.login1.Error.OperationInProgress so clients get a
// standard failure code.
func (m *manager) PowerOff(interactive bool) *dbus.Error { return runShutdown("poweroff") }
func (m *manager) Reboot(interactive bool) *dbus.Error   { return runShutdown("reboot") }
func (m *manager) Halt(interactive bool) *dbus.Error     { return runShutdown("halt") }
func (m *manager) Suspend(interactive bool) *dbus.Error  { return writeSysPower("mem") }
func (m *manager) Hibernate(interactive bool) *dbus.Error {
	return writeSysPower("disk")
}
func (m *manager) HybridSleep(interactive bool) *dbus.Error {
	return writeSysPower("disk")
}
func (m *manager) SuspendThenHibernate(interactive bool) *dbus.Error {
	return writeSysPower("mem")
}

// *WithFlags variants — systemd 246+ / elogind 246+ shape. GDM and
// modern lightdm both call the WithFlags forms exclusively; without
// them the reboot/shutdown buttons in the greeter fail silently with
// UnknownMethod and the operator has to drop to a TTY. flags is a
// bitfield (SD_LOGIND_ROOT_CHECK_INHIBITORS etc.) — we don't consult
// inhibitors yet, so the flag argument is accepted and ignored.
func (m *manager) PowerOffWithFlags(flags uint64) *dbus.Error { return runShutdown("poweroff") }
func (m *manager) RebootWithFlags(flags uint64) *dbus.Error   { return runShutdown("reboot") }
func (m *manager) HaltWithFlags(flags uint64) *dbus.Error     { return runShutdown("halt") }
func (m *manager) SuspendWithFlags(flags uint64) *dbus.Error  { return writeSysPower("mem") }
func (m *manager) HibernateWithFlags(flags uint64) *dbus.Error {
	return writeSysPower("disk")
}
func (m *manager) HybridSleepWithFlags(flags uint64) *dbus.Error {
	return writeSysPower("disk")
}
func (m *manager) SuspendThenHibernateWithFlags(flags uint64) *dbus.Error {
	return writeSysPower("mem")
}

// Sleep is the systemd 253+ dispatcher — the client asks the daemon
// to pick the best sleep operation given the hardware. We proxy to
// Suspend as the safest default.
func (m *manager) Sleep(flags uint64) *dbus.Error { return writeSysPower("mem") }
func (m *manager) CanSleep() (string, *dbus.Error) { return canSleep("mem"), nil }

// Reload is called by `loginctl reload` (used e.g. by
// /etc/gdm/custom.conf edits when the operator drops in new drop-ins).
// We have no persistent config to re-read yet, so this is a no-op
// success — clients that call it expect ACK, not implementation.
func (m *manager) Reload() *dbus.Error { return nil }

// Can<Op> methods answer whether the corresponding action is
// available. systemd returns "yes"/"no"/"challenge"/"na". slinit-
// logind returns "yes" whenever the underlying capability exists,
// "na" otherwise; polkit-style challenges are out of scope until we
// wire an authorization backend.
func (m *manager) CanPowerOff() (string, *dbus.Error) { return canShutdown(), nil }
func (m *manager) CanReboot() (string, *dbus.Error)   { return canShutdown(), nil }
func (m *manager) CanHalt() (string, *dbus.Error)     { return canShutdown(), nil }
func (m *manager) CanSuspend() (string, *dbus.Error)  { return canSleep("mem"), nil }
func (m *manager) CanHibernate() (string, *dbus.Error) {
	return canSleep("disk"), nil
}
func (m *manager) CanHybridSleep() (string, *dbus.Error) {
	return canSleep("disk"), nil
}
func (m *manager) CanSuspendThenHibernate() (string, *dbus.Error) {
	return canSleep("mem"), nil
}

// Inhibit takes (what, who, why, mode). systemd returns a duplicated
// file descriptor the caller must keep open until the inhibitor is
// released. Phase A returns a pipe read-end so client code that
// checks-then-uses the fd works; real enforcement (checking on
// PowerOff/Suspend before actioning) lands with the inhibitor
// registry in Phase C.
func (m *manager) Inhibit(what, who, why, mode string) (dbus.UnixFD, *dbus.Error) {
	r, w, err := os.Pipe()
	if err != nil {
		return 0, dbus.NewError("org.freedesktop.login1.Error.PipeCreationFailed",
			[]interface{}{err.Error()})
	}
	// The client keeps the read-end; we drop the write-end so that
	// when the client closes the fd there's no leak on our side.
	_ = w.Close()
	return dbus.UnixFD(r.Fd()), nil
}

// --- helpers ---

func sessionPath(id string) dbus.ObjectPath {
	return dbus.ObjectPath(objPath + "/session/" + dbusMangle(id))
}
func userPath(uid uint32) dbus.ObjectPath {
	return dbus.ObjectPath(fmt.Sprintf("%s/user/_%d", objPath, uid))
}
func seatPath(id string) dbus.ObjectPath {
	return dbus.ObjectPath(objPath + "/seat/" + dbusMangle(id))
}

// dbusMangle escapes ids so they're safe as object-path components.
// systemd uses _<hex> for anything outside [A-Za-z0-9]; we do the
// same so downstream tooling parses our paths identically.
func dbusMangle(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') {
			out = append(out, c)
		} else {
			out = append(out, []byte(fmt.Sprintf("_%02x", c))...)
		}
	}
	return string(out)
}

func readJSON(path string, out interface{}) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, out) == nil
}

// runShutdown execs slinit-shutdown for the given action. Errors are
// mapped to a systemd-compatible D-Bus error name.
//
// The verb-to-flag mapping matters: slinit-shutdown parses its argv
// as short/long flags (`-r`, `--reboot`, `-p`, ...), NOT as
// systemctl-style verbs (`slinit-shutdown reboot` prints
// "Unrecognized option: reboot" and exits non-zero). An earlier
// revision passed verbs; slinit-shutdown ran, failed the parse, and
// exited fast — but exec.Command.Start() only reports fork/exec
// errors, not the child's exit status, so the D-Bus call still
// returned success. XFCE's Restart button would then trigger
// `xfce4-session` to close (thinking the reboot was in flight) but
// the machine stayed up, so the user got a silent logout instead of
// a reboot. Map verbs to the actual flags to fix that.
func runShutdown(verb string) *dbus.Error {
	bin := slinitctlPath
	if bin == "" {
		p, err := exec.LookPath("slinit-shutdown")
		if err != nil {
			return dbus.NewError("org.freedesktop.login1.Error.OperationInProgress",
				[]interface{}{"slinit-shutdown not found: " + err.Error()})
		}
		bin = p
	}
	var flag string
	switch verb {
	case "reboot":
		flag = "-r"
	case "halt":
		flag = "-h"
	case "poweroff":
		flag = "-p"
	default:
		return dbus.NewError("org.freedesktop.login1.Error.OperationInProgress",
			[]interface{}{"unknown shutdown verb: " + verb})
	}
	if err := exec.Command(bin, flag).Start(); err != nil {
		return dbus.NewError("org.freedesktop.login1.Error.OperationInProgress",
			[]interface{}{err.Error()})
	}
	return nil
}

// writeSysPower drops the requested state token into /sys/power/state,
// which is the kernel's sleep entry point. systemd-logind does the
// same after coordinating inhibitors + PAM notifications; our
// inhibitor registry is Phase C, so this is the minimal path.
func writeSysPower(state string) *dbus.Error {
	if err := os.WriteFile("/sys/power/state", []byte(state), 0); err != nil {
		return dbus.NewError("org.freedesktop.login1.Error.SleepNotSupported",
			[]interface{}{err.Error()})
	}
	return nil
}

func canShutdown() string {
	if slinitctlPath != "" {
		return "yes"
	}
	if _, err := exec.LookPath("slinit-shutdown"); err == nil {
		return "yes"
	}
	return "na"
}

// canSleep probes /sys/power/state for the given token. Kernels
// without CONFIG_SUSPEND / CONFIG_HIBERNATION expose a shorter list;
// we advertise "na" instead of lying about a state the kernel
// refuses to enter.
func canSleep(state string) string {
	b, err := os.ReadFile("/sys/power/state")
	if err != nil {
		return "na"
	}
	for _, tok := range splitFields(string(b)) {
		if tok == state {
			return "yes"
		}
	}
	return "na"
}

func splitFields(s string) []string {
	var out []string
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

func main() {
	debug := flag.Bool("debug", false, "log D-Bus method dispatch to stderr")
	userMode := flag.Bool("user", false, "session-bus mode: only host org.freedesktop.systemd1 stub for per-user auto-activation (skips login1 + /run/systemd tree)")
	flag.Parse()

	// --debug: redirect os.Stderr to /var/log/slinit-logind.log so the
	// method-dispatch prints and the session-machinery diagnostics
	// (cgroup migration path, CreateSession chain, ...) survive
	// slinit's runner attaching fd 2 to /dev/null. Ignored when the
	// file can't be opened; then debug output just goes to /dev/null
	// like it did before, which is fine for the normal --debug=false
	// path anyway.
	if *debug && !*userMode {
		if f, err := os.OpenFile("/var/log/slinit-logind.log",
			os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644); err == nil {
			os.Stderr = f
		}
	}

	// --user session-bus mode: connect to the caller's DBUS_SESSION_BUS
	// (dbus-daemon --session, activated by gnome-session or an
	// XDG_SESSION_TYPE=x11/wayland startup), register the systemd1
	// compat stub only, and block until the session bus goes away.
	// login1 doesn't make sense here — session bus is per-user, no
	// hardware/power authority — so we skip its registration and the
	// /run/systemd tree entirely.
	if *userMode {
		userMain(*debug)
		return
	}

	// Ensure state dirs exist so PAM's later create-session writes
	// don't have to race on mkdir.
	for _, d := range []string{"sessions", "users", "seats", "inhibitors"} {
		if err := os.MkdirAll(filepath.Join(stateRoot, d), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-logind: mkdir %s: %v\n", d, err)
			os.Exit(1)
		}
	}
	// libelogind/libsystemd compat directory tree — desktop stacks
	// (gdm, gnome-shell, xdg-desktop-portal) probe /run/systemd/*
	// at startup and refuse to spawn a greeter if the tree doesn't
	// exist. Mirror what elogind's daemon does on first launch.
	//
	// NOTE: we deliberately do NOT create /run/systemd/system —
	// that's the sd_booted() beacon, and if it exists,
	// gnome-session-binary takes the "under systemd" path where it
	// expects a real user manager to actually spawn the units in
	// gnome-login.session. Our systemd1 stub reports StartUnit as
	// succeeded but does nothing, so nothing runs — no gnome-shell,
	// no mutter, no greeter. With the beacon absent, sd_booted()
	// returns false and gnome-session-binary falls into standalone
	// autostart mode: it reads the .session file itself and
	// spawns each RequiredComponent as a direct child process, which
	// is what actually works on Sunlight.
	for _, d := range []string{"sessions", "users", "seats", "machines", "inaccessible"} {
		_ = os.MkdirAll(filepath.Join("/run/systemd", d), 0755)
	}
	// Inaccessible files bind-mounted by services that request
	// PrivateDevices / InaccessibleDirectories. Elogind creates these
	// as immutable placeholders — we do the same so a systemd unit
	// migrated to slinit doesn't fail on the bind-mount step.
	_ = os.WriteFile("/run/systemd/inaccessible/reg",
		[]byte{}, 0000)
	for _, kind := range []string{"blk", "chr", "sock", "fifo"} {
		_ = os.Remove("/run/systemd/inaccessible/" + kind)
	}
	_ = os.Mkdir("/run/systemd/inaccessible/dir", 0000)

	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: connect system bus: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	m := &manager{conn: conn, debug: *debug}
	if err := conn.Export(m, dbus.ObjectPath(objPath), iface); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: export: %v\n", err)
		os.Exit(1)
	}
	// Manager-path introspection — desktop stacks (XFCE session, gvfs,
	// portals) probe /org/freedesktop/login1 for its interface listing
	// before calling methods; without a valid Introspect response they
	// treat the daemon as absent and fall back into degraded modes
	// (broken menus, missing power controls, etc.).
	m.registerManagerIntrospection()
	// Read the foreground VT before exporting anything, so sessions and
	// seat0 are born with the right Active / ActiveSession values.
	if vt, ok := readActiveVT(); ok {
		currentVT.Store(vt)
	}
	// Re-register per-object exports for any sessions that persisted
	// across a slinit-logind restart. Without this loginctl show-session
	// would 404 on the object path for sessions that PAM created before
	// we came up.
	m.rehydrateObjects()
	// Session state lives in /run, so it outlives us. Drop whatever
	// belongs to a leader that is already gone, and watch the leaders
	// that are still alive.
	m.reapDeadSessions()
	// Ensure the always-on seat0 object exists so `loginctl seat-status`
	// works out of the box.
	m.ensureSeat("seat0")
	// Session activity follows the foreground VT from here on. Started
	// after ensureSeat so the first refresh has a seat to update.
	go m.watchActiveVT()
	// The self/auto session paths. gnome-shell's greeter has no
	// XDG_SESSION_ID and reaches for .../session/auto to find itself.
	m.registerSessionAliases()

	// Request the well-known bus name. RequestNameFlagReplaceExisting
	// makes us take over from elogind on a running system without
	// requiring elogind to release the name first — Phase B rip-and-
	// replace will remove elogind entirely.
	reply, err := conn.RequestName(busName,
		dbus.NameFlagAllowReplacement|dbus.NameFlagReplaceExisting)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: request name: %v\n", err)
		os.Exit(1)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		fmt.Fprintf(os.Stderr, "slinit-logind: not primary owner of %s (reply=%d) — is elogind holding the name with AllowReplacement=false?\n", busName, reply)
		os.Exit(1)
	}

	if *debug {
		fmt.Fprintf(os.Stderr, "slinit-logind: registered as %s at %s\n", busName, objPath)
	}

	// systemd1-compat stub — hosts org.freedesktop.systemd1 on the
	// same connection so gnome-session-binary and gdm-x-session can
	// complete their startup handshake. Fails soft: if the name is
	// already owned (live systemd, another shim), we log and keep
	// login1 running.
	_ = registerSystemd1(conn, *debug)

	// Block on SIGTERM/SIGINT. Nothing else to do — dbus/v5 runs its
	// own dispatch goroutine.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}
