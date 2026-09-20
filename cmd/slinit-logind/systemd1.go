// systemd1.go — minimum-viable org.freedesktop.systemd1 stub, hosted
// alongside the login1 daemon on the same system-bus connection.
//
// GNOME's gnome-session-binary and gdm-x-session make ~8-12 calls into
// org.freedesktop.systemd1 during session startup (mostly
// SetEnvironment + GetUnit + StartUnit for user units). Without an
// owner of the name, every call returns
// `org.freedesktop.DBus.Error.ServiceUnknown` and the session-init
// path aborts before painting the shell.
//
// slinit isn't systemd — we do not actually manage user units under
// systemd's unit model. The stub takes the pragmatic path:
//
//   - env-mutation methods (Set/Unset/UnsetAndSetEnvironment) store
//     the payload in-memory and expose it via the `Environment`
//     property. GNOME uses this to publish DISPLAY / XDG_* / DBUS
//     addresses to future units it thinks it will start.
//   - unit-lookup methods (GetUnit, LoadUnit, GetUnitByPID) return an
//     ObjectPath under /org/freedesktop/systemd1/unit/<escaped>. The
//     unit object at that path answers ActiveState="active" and
//     LoadState="loaded" for anything asked — enough to convince the
//     caller the unit is up and running.
//   - job-shaped methods (StartUnit, StopUnit, RestartUnit, ReloadUnit,
//     ReloadOrRestartUnit) return a fake job ObjectPath and
//     immediately emit JobRemoved(id, path, unit, "done") so the
//     caller's wait loop wakes up thinking the job succeeded.
//   - Subscribe/Unsubscribe are noops that return success.
//
// This is deliberately not a systemd emulator. It exists to unblock
// desktop-session bootstrap and give gnome-session-binary the
// handshake it needs. Callers that expect actual unit management
// (systemctl start foo.service) will not see foo.service start — but
// nothing on Sunlight's boot path uses systemctl-style unit control,
// so nothing regresses.

package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	systemd1BusName = "org.freedesktop.systemd1"
	systemd1ObjPath = "/org/freedesktop/systemd1"
	systemd1Iface   = "org.freedesktop.systemd1.Manager"
	systemd1UnitIf  = "org.freedesktop.systemd1.Unit"
)

// systemd1Manager holds the stub's mutable state — an environment
// map, a per-unit "started" bit, and a monotonically-increasing job
// counter.
//
// The per-unit map is important: gnome-session-binary probes
// `GetUnit("gnome-session-manager.service")` BEFORE it starts its
// own manager, and if the stub reports it as ActiveState=active
// then gnome-session-binary decides another manager already owns the
// session and exits with "Session manager already running!". So the
// stub must report a unit as active only AFTER a StartUnit has been
// issued for it — which is the systemd contract anyway.
type systemd1Manager struct {
	conn      *dbus.Conn
	mu        sync.Mutex
	env       map[string]string // KEY -> "KEY=VALUE"
	started   map[string]bool   // unit name -> has been StartUnit'd
	nextJ     uint64            // next job id, atomic-accessed
	leaderMon uint32            // gnome-session-ctl-monitor compat: 0=off, 1=goroutine running
	debug     bool              // set to true to log every dispatched call
}

// dbg is a one-liner tracer for stub calls. Enabled when the daemon
// was started with --debug; the output goes to slinit's catch-all
// log (slinit-logind runs under slinit's runner which captures
// stderr). Used only during interop debugging, so the format is
// deliberately terse.
func (s *systemd1Manager) dbg(format string, args ...any) {
	if !s.debug {
		return
	}
	fmt.Fprintf(os.Stderr, "systemd1-stub: "+format+"\n", args...)
}

func (s *systemd1Manager) isActive(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started[name]
}

func (s *systemd1Manager) setActive(name string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started == nil {
		s.started = make(map[string]bool)
	}
	if on {
		s.started[name] = true
	} else {
		delete(s.started, name)
	}
}

// escapeUnitName converts a systemd unit name (e.g.
// "gnome-shell-x11.service") to the byte-escaped form systemd uses
// in ObjectPaths: allowed [A-Za-z0-9_], everything else becomes
// _<hex>. Matches systemd's bus_path_escape.
func escapeUnitName(name string) string {
	var b strings.Builder
	b.Grow(len(name) * 2)
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c)
		case c >= 'a' && c <= 'z':
			b.WriteByte(c)
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		case c == '_':
			b.WriteByte('_')
		default:
			fmt.Fprintf(&b, "_%02x", c)
		}
	}
	return b.String()
}

func (s *systemd1Manager) unitPath(name string) dbus.ObjectPath {
	return dbus.ObjectPath(systemd1ObjPath + "/unit/" + escapeUnitName(name))
}

func (s *systemd1Manager) jobPath() dbus.ObjectPath {
	id := atomic.AddUint64(&s.nextJ, 1)
	return dbus.ObjectPath(fmt.Sprintf("%s/job/%d", systemd1ObjPath, id))
}

// -----------------------------------------------------------------------------
// Environment methods
// -----------------------------------------------------------------------------

// SetEnvironment merges the given "KEY=VALUE" strings into the
// manager's env. Idempotent; last write wins.
func (s *systemd1Manager) SetEnvironment(assignments []string) *dbus.Error {
	s.dbg("SetEnvironment(%d entries)", len(assignments))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, kv := range assignments {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		s.env[kv[:eq]] = kv
	}
	return nil
}

// UnsetEnvironment drops any key that appears in the argument list.
// Values-in-args are ignored — this matches systemd's semantics.
func (s *systemd1Manager) UnsetEnvironment(names []string) *dbus.Error {
	s.dbg("UnsetEnvironment(%d entries)", len(names))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range names {
		if eq := strings.IndexByte(n, '='); eq > 0 {
			n = n[:eq]
		}
		delete(s.env, n)
	}
	return nil
}

// UnsetAndSetEnvironment atomically clears then sets — used by
// gnome-session to swap its whole env in one shot.
func (s *systemd1Manager) UnsetAndSetEnvironment(unset, set []string) *dbus.Error {
	s.dbg("UnsetAndSetEnvironment(unset=%d, set=%d)", len(unset), len(set))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range unset {
		if eq := strings.IndexByte(n, '='); eq > 0 {
			n = n[:eq]
		}
		delete(s.env, n)
	}
	for _, kv := range set {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		s.env[kv[:eq]] = kv
	}
	return nil
}

// -----------------------------------------------------------------------------
// Unit-lookup methods — return a stub ObjectPath. The unit object at
// that path is registered lazily via registerUnitObject.
// -----------------------------------------------------------------------------

// GetUnit returns the object path for a unit that has been previously
// loaded or started. If the unit is unknown, return NoSuchUnit so
// callers (gnome-session-binary, gdm-x-session) treat it as absent
// and proceed with their own startup instead of thinking it's
// already up. This matches systemd's semantics: GetUnit is a lookup,
// not a load — LoadUnit is the load call.
func (s *systemd1Manager) GetUnit(name string) (dbus.ObjectPath, *dbus.Error) {
	if !s.isActive(name) {
		s.dbg("GetUnit(%q) -> NoSuchUnit", name)
		return "", dbus.NewError(
			"org.freedesktop.systemd1.NoSuchUnit",
			[]any{"Unit " + name + " not loaded."})
	}
	s.dbg("GetUnit(%q) -> ok", name)
	p := s.unitPath(name)
	s.registerUnitObject(name, p)
	return p, nil
}

// LoadUnit registers the unit object path (returning success) but
// doesn't mark it active — the unit is "loaded but not started".
// Matches systemd where LoadUnit succeeds for any nameable unit
// without changing its ActiveState.
func (s *systemd1Manager) LoadUnit(name string) (dbus.ObjectPath, *dbus.Error) {
	s.dbg("LoadUnit(%q)", name)
	p := s.unitPath(name)
	s.registerUnitObject(name, p)
	return p, nil
}

// GetUnitByPID returns NoSuchUnit — slinit doesn't track units by pid
// and pretending we do would let callers think an ambient scope is
// active when nothing is. Callers that hit this fall back to a
// looser path (usually reading /proc/PID/cgroup themselves).
func (s *systemd1Manager) GetUnitByPID(pid uint32) (dbus.ObjectPath, *dbus.Error) {
	s.dbg("GetUnitByPID(%d) -> NoSuchUnit", pid)
	return "", dbus.NewError(
		"org.freedesktop.systemd1.NoSuchUnit",
		[]any{"No unit for PID."})
}

// GetUnitByPIDFD is the pidfd-carrying form systemd 253+ added, and the
// one polkitd reaches for first when resolving a caller's unit. Without
// it polkitd takes an UnknownMethod error on every authorisation check
// before falling back — answering NoSuchUnit sends it down the same
// fallback path as GetUnitByPID, without the error.
func (s *systemd1Manager) GetUnitByPIDFD(pidfd dbus.UnixFD) (dbus.ObjectPath, *dbus.Error) {
	s.dbg("GetUnitByPIDFD(fd=%d) -> NoSuchUnit", int(pidfd))
	return "", dbus.NewError(
		"org.freedesktop.systemd1.NoSuchUnit",
		[]any{"No unit for PID."})
}

func (s *systemd1Manager) GetUnitByInvocationID(id []byte) (dbus.ObjectPath, *dbus.Error) {
	return "", dbus.NewError(
		"org.freedesktop.systemd1.NoSuchUnit",
		[]any{"No unit for invocation ID."})
}

// -----------------------------------------------------------------------------
// Job-shaped methods — return a fake job path and immediately emit
// JobRemoved so the caller's wait loop wakes up thinking the job
// completed successfully.
// -----------------------------------------------------------------------------

func (s *systemd1Manager) startJob(name, verb string) (dbus.ObjectPath, *dbus.Error) {
	s.dbg("%s(%q)", verb, name)
	// Route gnome-session's own top-level targets into gnome-session-
	// binary's non-systemd fallback path. Reporting success on
	// StartUnit + emitting only JobRemoved isn't enough to satisfy
	// gnome-session-binary's post-start wait: it expects a full
	// systemd signal stream (UnitNew, PropertiesChanged with
	// ActiveState=active on the unit path, JobNew, Reloading...) —
	// our stub only emits JobRemoved. After ~10 s of silence it
	// prints "Session termination requested" and unwinds.
	//
	// The `Falling back to non-systemd startup procedure due to
	// error: %s` code path in gnome-session's gsm-manager.c triggers
	// on any StartUnit error and does the legacy autostart flow
	// instead: read gnome-login.session's RequiredComponents, exec
	// each .desktop's Exec directly. That's exactly what Chimera
	// Linux forces at compile time with -Dsystemduserunitdir=/tmp.
	// Returning a specific error here reaches the same end state
	// without patching gnome-session.
	if strings.HasPrefix(name, "gnome-session-") && verb == "start" {
		s.dbg("%s(%q) -> LoadFailed (steer to non-systemd fallback)", verb, name)
		return "", dbus.NewError(
			"org.freedesktop.systemd1.LoadFailed",
			[]any{"slinit-logind stub does not drive gnome-session targets; falling back to autostart"})
	}
	p := s.jobPath()
	// job id is the last path component.
	base := string(p)
	slash := strings.LastIndexByte(base, '/')
	var jobID uint64
	fmt.Sscanf(base[slash+1:], "%d", &jobID)

	// Emit JobRemoved(id, job_path, unit_name, result) asynchronously
	// so the caller's mainloop dispatches our reply first.
	go func() {
		_ = s.conn.Emit(dbus.ObjectPath(systemd1ObjPath),
			systemd1Iface+".JobRemoved",
			uint32(jobID), p, name, "done")
	}()
	_ = verb // logged in principle; noop here
	return p, nil
}

func (s *systemd1Manager) StartUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	s.setActive(name, true)
	s.maybeSpawnSessionLeaderMonitor(name)
	return s.startJob(name, "start")
}
func (s *systemd1Manager) StartUnitWithFlags(name, mode string, flags uint64) (dbus.ObjectPath, *dbus.Error) {
	s.setActive(name, true)
	s.maybeSpawnSessionLeaderMonitor(name)
	return s.startJob(name, "start")
}

// maybeSpawnSessionLeaderMonitor mirrors what gnome-session-ctl
// --monitor does in a systemd-managed setup: hold the read end of
// $XDG_RUNTIME_DIR/gnome-session-leader-fifo so gnome-session-binary's
// later O_WRONLY|O_CLOEXEC open on the same path unblocks.
//
// Background — under a real systemd stack, `gnome-session-manager@
// gnome.target` (started via StartUnit) pulls in `gnome-session-ctl
// --monitor` as a dependency; that helper opens the FIFO read end
// and blocks on read, then triggers `gnome-session-shutdown.target`
// on EOF. Our stub's StartUnit is inert — no dependency chain fires —
// so nothing opens the read end and gnome-session-binary deadlocks
// on its own O_WRONLY open.
//
// We compensate by opening the FIFO read end ourselves on the first
// StartUnit that names a gnome-session target. Held for the lifetime
// of the (per-session) --user daemon; EOF (peer closes write end at
// shutdown) is expected and cleanly logged.
func (s *systemd1Manager) maybeSpawnSessionLeaderMonitor(unit string) {
	if !strings.HasPrefix(unit, "gnome-session-") {
		return
	}
	// Idempotency — only start one monitor goroutine.
	if !atomic.CompareAndSwapUint32(&s.leaderMon, 0, 1) {
		return
	}
	go s.holdSessionLeaderFIFO()
}

func (s *systemd1Manager) holdSessionLeaderFIFO() {
	xdg := os.Getenv("XDG_RUNTIME_DIR")
	if xdg == "" {
		return
	}
	path := filepath.Join(xdg, "gnome-session-leader-fifo")
	// Poll for the FIFO to appear — gnome-session-binary mkfifo's it
	// after StartUnit succeeds, so we may briefly race. 20 attempts
	// * 50ms = 1 s ceiling; that's ~10x margin over the observed
	// gnome-session-binary latency between StartUnit and mkfifo.
	var f *os.File
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(path); err == nil {
			// Open O_RDONLY WITHOUT O_NONBLOCK — blocks until a
			// writer arrives, but that's fine: gnome-session-binary
			// is about to become the writer, and our block resolves
			// as soon as it does.
			ff, err := os.OpenFile(path, os.O_RDONLY, 0)
			if err == nil {
				f = ff
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if f == nil {
		return
	}
	defer f.Close()
	// Drain the FIFO — reads block until the writer closes or sends a
	// byte. On EOF (writer close at shutdown) we return; the deferred
	// Close releases the fd.
	_, _ = io.Copy(io.Discard, f)
}
func (s *systemd1Manager) StopUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	s.setActive(name, false)
	return s.startJob(name, "stop")
}
func (s *systemd1Manager) RestartUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	s.setActive(name, true)
	return s.startJob(name, "restart")
}
func (s *systemd1Manager) ReloadUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	return s.startJob(name, "reload")
}
func (s *systemd1Manager) ReloadOrRestartUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	s.setActive(name, true)
	return s.startJob(name, "reload-or-restart")
}
func (s *systemd1Manager) TryRestartUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	// try-restart is a nop if the unit isn't already active.
	if s.isActive(name) {
		return s.startJob(name, "try-restart")
	}
	return s.startJob(name, "try-restart")
}
func (s *systemd1Manager) ReloadOrTryRestartUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	return s.startJob(name, "reload-or-try-restart")
}

// KillUnit / KillUnitSubgroup / ResetFailedUnit / ResetFailed are
// noops — return success so callers don't fail on cleanup paths.
// gnome-session-binary calls `ResetFailed()` (no args, resets ALL
// failed units) during greeter startup — it's one of the first
// systemd1 calls after activation, and without it gnome-session
// abandons startup with "Failed to reset failed state of units".
func (s *systemd1Manager) KillUnit(name, who string, sig int32) *dbus.Error {
	s.dbg("KillUnit(%q, %q, %d)", name, who, sig)
	return nil
}
func (s *systemd1Manager) ResetFailedUnit(name string) *dbus.Error {
	s.dbg("ResetFailedUnit(%q)", name)
	return nil
}
func (s *systemd1Manager) ResetFailed() *dbus.Error { s.dbg("ResetFailed"); return nil }
func (s *systemd1Manager) ClearJobs() *dbus.Error   { s.dbg("ClearJobs"); return nil }
func (s *systemd1Manager) SetUnitProperties(name string, runtime bool, props []struct {
	Name  string
	Value dbus.Variant
}) *dbus.Error {
	return nil
}

// -----------------------------------------------------------------------------
// Subscribe / Unsubscribe — signal-firehose management. GNOME calls
// Subscribe() during startup and expects it to succeed; without a
// subscription the daemon is nominally free to skip JobRemoved
// signals for that client, but we emit them unconditionally, so
// Subscribe is a pure success stub.
// -----------------------------------------------------------------------------

func (s *systemd1Manager) Subscribe() *dbus.Error   { s.dbg("Subscribe"); return nil }
func (s *systemd1Manager) Unsubscribe() *dbus.Error { s.dbg("Unsubscribe"); return nil }
func (s *systemd1Manager) Reload() *dbus.Error      { s.dbg("Reload"); return nil }
func (s *systemd1Manager) Reexecute() *dbus.Error   { s.dbg("Reexecute"); return nil }

// -----------------------------------------------------------------------------
// List methods — return empty arrays. GNOME doesn't rely on these to
// find its units (it uses GetUnit/StartUnit); returning [] is safer
// than fabricating an inventory.
// -----------------------------------------------------------------------------

// UnitStatus mirrors the wire tuple systemd returns from ListUnits:
// (ssssssouso). We ship an empty slice.
type unitStatus struct {
	Name          string
	Description   string
	LoadState     string
	ActiveState   string
	SubState      string
	Following     string
	Unit          dbus.ObjectPath
	JobID         uint32
	JobType       string
	Job           dbus.ObjectPath
}

func (s *systemd1Manager) ListUnits() ([]unitStatus, *dbus.Error) {
	return []unitStatus{}, nil
}
func (s *systemd1Manager) ListUnitsFiltered(states []string) ([]unitStatus, *dbus.Error) {
	return []unitStatus{}, nil
}
func (s *systemd1Manager) ListUnitsByNames(names []string) ([]unitStatus, *dbus.Error) {
	return []unitStatus{}, nil
}
func (s *systemd1Manager) ListUnitsByPatterns(states, patterns []string) ([]unitStatus, *dbus.Error) {
	return []unitStatus{}, nil
}

type unitFile struct {
	Path  string
	State string
}

func (s *systemd1Manager) ListUnitFiles() ([]unitFile, *dbus.Error) {
	return []unitFile{}, nil
}
func (s *systemd1Manager) ListUnitFilesByPatterns(states, patterns []string) ([]unitFile, *dbus.Error) {
	return []unitFile{}, nil
}

func (s *systemd1Manager) GetDefaultTarget() (string, *dbus.Error) {
	return "graphical.target", nil
}
func (s *systemd1Manager) GetUnitFileState(name string) (string, *dbus.Error) {
	return "enabled", nil
}

// -----------------------------------------------------------------------------
// Manager properties — served via the DBus Properties interface. GNOME
// reads `Environment` after each SetEnvironment to confirm the merge.
// -----------------------------------------------------------------------------

type systemd1Props struct {
	mgr *systemd1Manager
}

func (p *systemd1Props) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	if iface != systemd1Iface {
		return dbus.Variant{}, dbus.NewError(
			"org.freedesktop.DBus.Error.UnknownInterface", nil)
	}
	switch prop {
	case "Version":
		return dbus.MakeVariant("slinit-logind (systemd1-compat stub)"), nil
	case "Features":
		return dbus.MakeVariant(""), nil
	case "Architecture":
		return dbus.MakeVariant("x86-64"), nil
	case "Tainted":
		return dbus.MakeVariant(""), nil
	case "SystemState":
		return dbus.MakeVariant("running"), nil
	case "Environment":
		p.mgr.mu.Lock()
		defer p.mgr.mu.Unlock()
		out := make([]string, 0, len(p.mgr.env))
		for _, kv := range p.mgr.env {
			out = append(out, kv)
		}
		return dbus.MakeVariant(out), nil
	case "NNames", "NFailedUnits", "NJobs", "NInstalledJobs", "NFailedJobs":
		return dbus.MakeVariant(uint32(0)), nil
	case "Progress":
		return dbus.MakeVariant(1.0), nil
	case "UnitPath":
		return dbus.MakeVariant([]string{}), nil
	case "ControlGroup":
		return dbus.MakeVariant(""), nil
	case "Virtualization", "ConfidentialVirtualization":
		return dbus.MakeVariant(""), nil
	}
	return dbus.Variant{}, dbus.NewError(
		"org.freedesktop.DBus.Properties.Error.PropertyNotFound", nil)
}

func (p *systemd1Props) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface == "" {
		iface = systemd1Iface
	}
	if iface != systemd1Iface {
		return nil, dbus.NewError(
			"org.freedesktop.DBus.Error.UnknownInterface", nil)
	}
	out := map[string]dbus.Variant{
		"Version":        dbus.MakeVariant("slinit-logind (systemd1-compat stub)"),
		"Features":       dbus.MakeVariant(""),
		"Architecture":   dbus.MakeVariant("x86-64"),
		"Tainted":        dbus.MakeVariant(""),
		"SystemState":    dbus.MakeVariant("running"),
		"NNames":         dbus.MakeVariant(uint32(0)),
		"NFailedUnits":   dbus.MakeVariant(uint32(0)),
		"NJobs":          dbus.MakeVariant(uint32(0)),
		"NInstalledJobs": dbus.MakeVariant(uint32(0)),
		"NFailedJobs":    dbus.MakeVariant(uint32(0)),
		"Progress":       dbus.MakeVariant(1.0),
		"UnitPath":       dbus.MakeVariant([]string{}),
		"ControlGroup":   dbus.MakeVariant(""),
	}
	p.mgr.mu.Lock()
	envSnap := make([]string, 0, len(p.mgr.env))
	for _, kv := range p.mgr.env {
		envSnap = append(envSnap, kv)
	}
	p.mgr.mu.Unlock()
	out["Environment"] = dbus.MakeVariant(envSnap)
	return out, nil
}

func (p *systemd1Props) Set(iface, prop string, value dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", nil)
}

// -----------------------------------------------------------------------------
// Per-unit stub. The registration is lazy — the first GetUnit/LoadUnit
// call for a given name installs its object at the correct path.
// -----------------------------------------------------------------------------

// systemd1UnitStub answers the tiny slice of the Unit interface GNOME
// actually reads: ActiveState / SubState / LoadState + Id / Names.
// State derives from the outer manager's `started` map — a unit
// reports active only after StartUnit has been called for it,
// matching systemd's contract.
type systemd1UnitStub struct {
	name string
	mgr  *systemd1Manager
}

func (u *systemd1UnitStub) activeState() (string, string) {
	if u.mgr.isActive(u.name) {
		return "active", "running"
	}
	return "inactive", "dead"
}

func (u *systemd1UnitStub) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	// Callers (glib GDBusProxy) occasionally probe with iface="" to
	// mean "the primary interface of this object"; tolerate it.
	if iface != systemd1UnitIf && iface != "" {
		return dbus.Variant{}, dbus.NewError(
			"org.freedesktop.DBus.Error.UnknownInterface", nil)
	}
	active, sub := u.activeState()
	switch prop {
	case "Id":
		return dbus.MakeVariant(u.name), nil
	case "Names":
		return dbus.MakeVariant([]string{u.name}), nil
	case "LoadState":
		return dbus.MakeVariant("loaded"), nil
	case "ActiveState":
		return dbus.MakeVariant(active), nil
	case "SubState":
		return dbus.MakeVariant(sub), nil
	case "Description":
		return dbus.MakeVariant("slinit-logind systemd1-compat stub"), nil
	case "FragmentPath", "SourcePath":
		return dbus.MakeVariant(""), nil
	case "UnitFileState":
		return dbus.MakeVariant("enabled"), nil
	case "UnitFilePreset":
		return dbus.MakeVariant("enabled"), nil
	case "CanStart", "CanStop", "CanReload", "CanIsolate":
		return dbus.MakeVariant(true), nil
	case "NeedDaemonReload":
		return dbus.MakeVariant(false), nil
	case "Following":
		return dbus.MakeVariant(""), nil
	case "Job":
		return dbus.MakeVariant(struct {
			ID   uint32
			Path dbus.ObjectPath
		}{0, dbus.ObjectPath("/")}), nil
	}
	return dbus.Variant{}, dbus.NewError(
		"org.freedesktop.DBus.Properties.Error.PropertyNotFound", nil)
}

func (u *systemd1UnitStub) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface == "" {
		iface = systemd1UnitIf
	}
	if iface != systemd1UnitIf {
		return nil, dbus.NewError(
			"org.freedesktop.DBus.Error.UnknownInterface", nil)
	}
	active, sub := u.activeState()
	return map[string]dbus.Variant{
		"Id":               dbus.MakeVariant(u.name),
		"Names":            dbus.MakeVariant([]string{u.name}),
		"LoadState":        dbus.MakeVariant("loaded"),
		"ActiveState":      dbus.MakeVariant(active),
		"SubState":         dbus.MakeVariant(sub),
		"Description":      dbus.MakeVariant("slinit-logind systemd1-compat stub"),
		"FragmentPath":     dbus.MakeVariant(""),
		"UnitFileState":    dbus.MakeVariant("enabled"),
		"UnitFilePreset":   dbus.MakeVariant("enabled"),
		"CanStart":         dbus.MakeVariant(true),
		"CanStop":          dbus.MakeVariant(true),
		"CanReload":        dbus.MakeVariant(true),
		"CanIsolate":       dbus.MakeVariant(true),
		"NeedDaemonReload": dbus.MakeVariant(false),
	}, nil
}

func (u *systemd1UnitStub) Set(iface, prop string, value dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", nil)
}

// registerUnitObject installs a per-unit stub at the given path.
// Idempotent — a repeat GetUnit for the same name is cheap.
func (s *systemd1Manager) registerUnitObject(name string, path dbus.ObjectPath) {
	// dbus.Conn.Export is idempotent enough for our purposes; a
	// re-export overwrites the handler and both point at the same
	// answers, so we don't dedup.
	unit := &systemd1UnitStub{name: name, mgr: s}
	_ = s.conn.Export(unit, path, systemd1UnitIf)
	_ = s.conn.Export(unit, path, "org.freedesktop.DBus.Properties")
	_ = s.conn.Export(
		introspect.Introspectable(systemd1UnitIntrospect(name, path)),
		path, "org.freedesktop.DBus.Introspectable")
}

// -----------------------------------------------------------------------------
// Introspection XML — desktop stacks probe Introspect before invoking
// methods. Without a valid interface listing, some callers (glib's
// GDBus, notably) treat the object as absent.
// -----------------------------------------------------------------------------

func systemd1ManagerIntrospect() string {
	return `<node>
  <interface name="` + systemd1Iface + `">
    <method name="SetEnvironment"><arg direction="in" type="as"/></method>
    <method name="UnsetEnvironment"><arg direction="in" type="as"/></method>
    <method name="UnsetAndSetEnvironment"><arg direction="in" type="as"/><arg direction="in" type="as"/></method>
    <method name="GetUnit"><arg direction="in" type="s"/><arg direction="out" type="o"/></method>
    <method name="LoadUnit"><arg direction="in" type="s"/><arg direction="out" type="o"/></method>
    <method name="GetUnitByPID"><arg direction="in" type="u"/><arg direction="out" type="o"/></method>
    <method name="StartUnit"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="out" type="o"/></method>
    <method name="StopUnit"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="out" type="o"/></method>
    <method name="RestartUnit"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="out" type="o"/></method>
    <method name="ReloadUnit"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="out" type="o"/></method>
    <method name="ReloadOrRestartUnit"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="out" type="o"/></method>
    <method name="TryRestartUnit"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="out" type="o"/></method>
    <method name="ReloadOrTryRestartUnit"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="out" type="o"/></method>
    <method name="KillUnit"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="in" type="i"/></method>
    <method name="ResetFailedUnit"><arg direction="in" type="s"/></method>
    <method name="ResetFailed"/>
    <method name="ClearJobs"/>
    <method name="Subscribe"/>
    <method name="Unsubscribe"/>
    <method name="Reload"/>
    <method name="Reexecute"/>
    <method name="ListUnits"><arg direction="out" type="a(ssssssouso)"/></method>
    <method name="ListUnitsFiltered"><arg direction="in" type="as"/><arg direction="out" type="a(ssssssouso)"/></method>
    <method name="ListUnitFiles"><arg direction="out" type="a(ss)"/></method>
    <method name="GetDefaultTarget"><arg direction="out" type="s"/></method>
    <method name="GetUnitFileState"><arg direction="in" type="s"/><arg direction="out" type="s"/></method>
    <signal name="JobRemoved"><arg type="u"/><arg type="o"/><arg type="s"/><arg type="s"/></signal>
    <signal name="UnitNew"><arg type="s"/><arg type="o"/></signal>
    <signal name="UnitRemoved"><arg type="s"/><arg type="o"/></signal>
    <signal name="Reloading"><arg type="b"/></signal>
    <property name="Version" type="s" access="read"/>
    <property name="Architecture" type="s" access="read"/>
    <property name="SystemState" type="s" access="read"/>
    <property name="Environment" type="as" access="read"/>
    <property name="NNames" type="u" access="read"/>
    <property name="NFailedUnits" type="u" access="read"/>
    <property name="NJobs" type="u" access="read"/>
  </interface>
  <interface name="org.freedesktop.DBus.Properties">
    <method name="Get"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="out" type="v"/></method>
    <method name="GetAll"><arg direction="in" type="s"/><arg direction="out" type="a{sv}"/></method>
    <method name="Set"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="in" type="v"/></method>
    <signal name="PropertiesChanged"><arg type="s"/><arg type="a{sv}"/><arg type="as"/></signal>
  </interface>
  <interface name="org.freedesktop.DBus.Introspectable">
    <method name="Introspect"><arg direction="out" type="s"/></method>
  </interface>
</node>`
}

func systemd1UnitIntrospect(name string, path dbus.ObjectPath) string {
	return `<node>
  <interface name="` + systemd1UnitIf + `">
    <property name="Id" type="s" access="read"/>
    <property name="Names" type="as" access="read"/>
    <property name="LoadState" type="s" access="read"/>
    <property name="ActiveState" type="s" access="read"/>
    <property name="SubState" type="s" access="read"/>
    <property name="Description" type="s" access="read"/>
    <property name="Following" type="s" access="read"/>
    <property name="Job" type="(uo)" access="read"/>
  </interface>
  <interface name="org.freedesktop.DBus.Properties">
    <method name="Get"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="out" type="v"/></method>
    <method name="GetAll"><arg direction="in" type="s"/><arg direction="out" type="a{sv}"/></method>
    <method name="Set"><arg direction="in" type="s"/><arg direction="in" type="s"/><arg direction="in" type="v"/></method>
  </interface>
</node>`
}

// -----------------------------------------------------------------------------
// Registration entry point, invoked from main() after the login1
// bootstrap succeeds. Fails soft — if the systemd1 name is already
// held (a live systemd or another compat shim), we log and continue
// without owning it so login1 stays functional.
// -----------------------------------------------------------------------------

func registerSystemd1(conn *dbus.Conn, debug bool) *systemd1Manager {
	s := &systemd1Manager{
		conn:    conn,
		env:     make(map[string]string, 32),
		started: make(map[string]bool, 32),
		debug:   debug,
	}

	if err := conn.Export(s, dbus.ObjectPath(systemd1ObjPath), systemd1Iface); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: export systemd1: %v\n", err)
		return nil
	}
	if err := conn.Export(&systemd1Props{mgr: s},
		dbus.ObjectPath(systemd1ObjPath),
		"org.freedesktop.DBus.Properties"); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: export systemd1 properties: %v\n", err)
		return nil
	}
	if err := conn.Export(introspect.Introspectable(systemd1ManagerIntrospect()),
		dbus.ObjectPath(systemd1ObjPath),
		"org.freedesktop.DBus.Introspectable"); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: export systemd1 introspect: %v\n", err)
		return nil
	}

	reply, err := conn.RequestName(systemd1BusName,
		dbus.NameFlagAllowReplacement|dbus.NameFlagReplaceExisting)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: systemd1 request name: %v\n", err)
		return nil
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		fmt.Fprintf(os.Stderr, "slinit-logind: not primary owner of %s (reply=%d) — continuing without systemd1-compat\n", systemd1BusName, reply)
		return nil
	}

	if debug {
		fmt.Fprintf(os.Stderr, "slinit-logind: registered as %s at %s (systemd1-compat stub)\n", systemd1BusName, systemd1ObjPath)
	}
	return s
}

// userMain runs slinit-logind in per-user session-bus mode. Invoked
// via `slinit-logind --user`, typically by dbus-daemon --session's
// activation of org.freedesktop.systemd1 when gnome-session-binary
// probes for it. Connects to the session bus (DBUS_SESSION_BUS_
// ADDRESS in env, populated by whatever spawned the session bus —
// dbus-run-session, dbus-launch, or gnome-session's transient
// dbus-daemon), registers the systemd1 stub, and blocks until
// SIGTERM/SIGINT.
//
// Nothing else is registered — no login1 (session bus is per-user,
// no hardware authority), no /run/systemd tree (that belongs to the
// system-bus daemon), no bus-name policy dance (session bus is
// per-user, dbus-daemon --session ships an `<allow own="*"/>`
// default for the owner-user context).
func userMain(debug bool) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind --user: connect session bus: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if s := registerSystemd1(conn, debug); s == nil {
		fmt.Fprintln(os.Stderr, "slinit-logind --user: systemd1 registration failed on session bus")
		os.Exit(1)
	}

	// Block on SIGTERM/SIGINT. dbus-daemon --session sends SIGTERM
	// to activated peers when the session bus shuts down (user
	// logout / DM stop), so we exit cleanly along with the session.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}
