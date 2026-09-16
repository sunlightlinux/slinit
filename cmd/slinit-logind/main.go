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
// callers can talk to per-object interfaces. Those interfaces are
// not exposed yet; the path resolves for symmetry with systemd.
func (m *manager) GetSession(id string) (dbus.ObjectPath, *dbus.Error) {
	return sessionPath(id), nil
}
func (m *manager) GetUser(uid uint32) (dbus.ObjectPath, *dbus.Error) {
	return userPath(uid), nil
}
func (m *manager) GetSeat(id string) (dbus.ObjectPath, *dbus.Error) {
	return seatPath(id), nil
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

// runShutdown execs slinit-shutdown with the given verb. Errors are
// mapped to a systemd-compatible D-Bus error name.
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
	if err := exec.Command(bin, verb).Start(); err != nil {
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
	flag.Parse()

	// Ensure state dirs exist so PAM's later create-session writes
	// don't have to race on mkdir.
	for _, d := range []string{"sessions", "users", "seats", "inhibitors"} {
		if err := os.MkdirAll(filepath.Join(stateRoot, d), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-logind: mkdir %s: %v\n", d, err)
			os.Exit(1)
		}
	}

	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: connect system bus: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	m := &manager{conn: conn}
	if err := conn.Export(m, dbus.ObjectPath(objPath), iface); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: export: %v\n", err)
		os.Exit(1)
	}
	// Re-register per-object exports for any sessions that persisted
	// across a slinit-logind restart. Without this loginctl show-session
	// would 404 on the object path for sessions that PAM created before
	// we came up.
	m.rehydrateObjects()
	// Ensure the always-on seat0 object exists so `loginctl seat-status`
	// works out of the box.
	m.ensureSeat("seat0")

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

	// Block on SIGTERM/SIGINT. Nothing else to do — dbus/v5 runs its
	// own dispatch goroutine.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}
