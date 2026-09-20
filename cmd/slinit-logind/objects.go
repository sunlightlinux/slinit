// Phase C — per-Session / User / Seat D-Bus objects.
//
// systemd's login1 model isn't a flat namespace on the Manager: each
// session, user and seat is a separate D-Bus object with its own
// properties + methods + signals. `loginctl show-session c1` queries
// the object at /org/freedesktop/login1/session/c1 for its property
// dict; without those objects it renders as blanks.
//
// The prop package auto-generates the DBus.Properties.Get/Set/GetAll
// handlers + introspection XML from the propsSpec below, so we only
// have to feed it the initial snapshot at register time and update
// the field whenever the record on disk changes.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	sessionIface = "org.freedesktop.login1.Session"
	userIface    = "org.freedesktop.login1.User"
	seatIface    = "org.freedesktop.login1.Seat"
)

// UserRef is the (uo) struct systemd uses for Session.User /
// User composite refs. Named type is required — passing an anonymous
// []interface{} makes godbus infer `av` (array of variant), which
// loginctl silently rejects when it expects a struct.
type UserRef struct {
	UID  uint32
	Path dbus.ObjectPath
}

// SeatRef is the (so) struct for Session.Seat / Seat.ActiveSession's
// display side.
type SeatRef struct {
	ID   string
	Path dbus.ObjectPath
}

// SessionRef is the (so) struct for User.Display / Sessions[i] /
// Seat.ActiveSession / Seat.Sessions[i].
type SessionRef struct {
	ID   string
	Path dbus.ObjectPath
}

// propsWrapper substitutes a primary interface name when the client
// passes an empty string to Get/GetAll/Set. systemd/elogind's loginctl
// calls `Properties.GetAll("")` to enumerate every property regardless
// of interface, and godbus's default prop.Properties handler responds
// with InterfaceNotFound for empty iface. Wrapping is cheaper than
// forking godbus's prop package.
type propsWrapper struct {
	props   *prop.Properties
	primary string
}

func (p *propsWrapper) Get(iface, property string) (dbus.Variant, *dbus.Error) {
	if iface == "" {
		iface = p.primary
	}
	return p.props.Get(iface, property)
}

func (p *propsWrapper) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface == "" {
		iface = p.primary
	}
	return p.props.GetAll(iface)
}

func (p *propsWrapper) Set(iface, property string, newVal dbus.Variant) *dbus.Error {
	if iface == "" {
		iface = p.primary
	}
	return p.props.Set(iface, property, newVal)
}

// exportPropsWrapper replaces the default Properties handler that
// prop.Export installed with our loginctl-friendly wrapper. Called
// right after each register* helper.
func (m *manager) exportPropsWrapper(path dbus.ObjectPath, props *prop.Properties, primary string) {
	w := &propsWrapper{props: props, primary: primary}
	_ = m.conn.Export(w, path, "org.freedesktop.DBus.Properties")
}

// rememberSessionProps keeps a session's exported property table so a
// later write can update a value and emit PropertiesChanged.
// prop.Export hands the handle back once and we used to drop it, which
// froze every property at its creation-time value.
func (m *manager) rememberSessionProps(id string, props *prop.Properties) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionProps == nil {
		m.sessionProps = map[string]*prop.Properties{}
	}
	m.sessionProps[id] = props
}

func (m *manager) forgetSessionProps(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessionProps, id)
}

// setSessionProp updates one property of a live session. No-op for a
// session we don't have exported — a client may well be writing to one
// that was just released.
func (m *manager) setSessionProp(id, name string, value any) {
	m.mu.RLock()
	props := m.sessionProps[id]
	m.mu.RUnlock()
	if props == nil {
		return
	}
	props.SetMust(sessionIface, name, value)
}

// sessionObject is the receiver for method calls on a session's D-Bus
// object. All methods delegate to the manager's Manager-scoped
// implementations so the per-object surface is a thin routing layer.
type sessionObject struct {
	m  *manager
	id string
}

// User object receiver — mirrors sessionObject's role.
type userObject struct {
	m   *manager
	uid uint32
}

// Seat object receiver.
type seatObject struct {
	m  *manager
	id string
}

// registerSessionObject exports the per-session D-Bus object at
// /org/freedesktop/login1/session/<id> with the standard set of
// Session properties + Terminate/Activate/Lock/Unlock/Kill methods.
// Emits SessionNew on the Manager path so subscribers get the same
// wake-up systemd delivers.
// sessionPropValues is the single source of truth for a session's
// property dict. Both the per-session object (via prop.Export) and the
// self/auto alias paths (which have to compute per caller) render from
// it, so the two can't drift apart.
func sessionPropValues(rec SessionRecord) map[string]any {
	return map[string]any{
		"Id":                     rec.ID,
		"User":                   userTuple(rec.UserID),
		"Name":                   rec.UserName,
		"Timestamp":              uint64(0),
		"VTNr":                   rec.VTNr,
		"Seat":                   seatTuple(rec.SeatID),
		"TTY":                    rec.TTY,
		"Display":                rec.Display,
		"Remote":                 rec.Remote,
		"RemoteHost":             rec.RemoteHost,
		"RemoteUser":             rec.RemoteUser,
		"Service":                rec.Service,
		"Leader":                 rec.LeaderPID,
		"Audit":                  uint32(0),
		"Type":                   rec.Type,
		"Class":                  rec.Class,
		"Active":                 true,
		"State":                  "active",
		"IdleHint":               false,
		"IdleSinceHint":          uint64(0),
		"IdleSinceHintMonotonic": uint64(0),
		"LockedHint":             false,
		"Scope":                  rec.Scope,
		"Desktop":                rec.Desktop,
	}
}

// sessionWritableProps are the ones a client may Set.
var sessionWritableProps = map[string]bool{
	"IdleHint": true, "LockedHint": true,
}

// sessionMutableProps change over a session's life, so they emit
// PropertiesChanged; everything else is fixed at creation.
var sessionMutableProps = map[string]bool{
	"Active": true, "State": true, "IdleHint": true,
	"IdleSinceHint": true, "IdleSinceHintMonotonic": true, "LockedHint": true,
}

func (m *manager) registerSessionObject(rec SessionRecord) {
	so := &sessionObject{m: m, id: rec.ID}
	path := sessionPath(rec.ID)

	propsSpec := map[string]map[string]*prop.Prop{sessionIface: {}}
	for name, val := range sessionPropValues(rec) {
		emit := prop.EmitConst
		if sessionMutableProps[name] {
			emit = prop.EmitTrue
		}
		propsSpec[sessionIface][name] = &prop.Prop{
			Value:    val,
			Writable: sessionWritableProps[name],
			Emit:     emit,
		}
	}
	props, err := prop.Export(m.conn, path, propsSpec)
	if err != nil {
		return
	}
	// Keep the handle so later writes (SetLockedHint, SetIdleHint) can
	// update the value and emit PropertiesChanged. Without it the
	// property dict is frozen at whatever the session looked like when
	// it was created.
	m.rememberSessionProps(rec.ID, props)
	// Override the default Properties handler so loginctl's empty-
	// interface GetAll succeeds.
	m.exportPropsWrapper(path, props, sessionIface)
	// Method handlers on the object itself.
	_ = m.conn.Export(so, path, sessionIface)

	// Introspection so `loginctl show-session` can enumerate.
	node := &introspect.Node{
		Name: string(path),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name:       sessionIface,
				Methods:    introspect.Methods(so),
				Properties: props.Introspection(sessionIface),
				// Lock / Unlock are how logind asks whoever owns the
				// session's screen to lock it or let go. gnome-shell's
				// screenShield lifts the lock screen from Unlock and
				// nothing else, so a client that introspects before
				// subscribing has to find them declared here.
				Signals: []introspect.Signal{
					{Name: "Lock"},
					{Name: "Unlock"},
				},
			},
		},
	}
	_ = m.conn.Export(introspect.NewIntrospectable(node), path,
		"org.freedesktop.DBus.Introspectable")

	// Fire SessionNew on the Manager. Wire signature per systemd:
	// (session_id: s, object_path: o).
	_ = m.conn.Emit(dbus.ObjectPath(objPath),
		iface+".SessionNew", rec.ID, path)
}

// unregisterSessionObject drops the object from the bus + fires
// SessionRemoved. Called from ReleaseSession.
func (m *manager) unregisterSessionObject(id string) {
	m.forgetSessionProps(id)
	path := sessionPath(id)
	_ = m.conn.Export(nil, path, sessionIface)
	_ = m.conn.Export(nil, path, "org.freedesktop.DBus.Properties")
	_ = m.conn.Export(nil, path, "org.freedesktop.DBus.Introspectable")
	_ = m.conn.Emit(dbus.ObjectPath(objPath),
		iface+".SessionRemoved", id, path)
}

// registerUserObject exports the per-user D-Bus object.
func (m *manager) registerUserObject(rec UserRecord) {
	uo := &userObject{m: m, uid: rec.UID}
	path := userPath(rec.UID)

	propsSpec := map[string]map[string]*prop.Prop{
		userIface: {
			"UID":         {Value: rec.UID, Emit: prop.EmitConst},
			"GID":         {Value: rec.UID, Emit: prop.EmitConst},
			"Name":        {Value: rec.Name, Emit: prop.EmitConst},
			"Timestamp":   {Value: uint64(0), Emit: prop.EmitConst},
			"RuntimePath": {Value: rec.RuntimePath, Emit: prop.EmitConst},
			"Service":     {Value: "", Emit: prop.EmitConst},
			"Slice":       {Value: rec.Slice, Emit: prop.EmitConst},
			"Display":     {Value: userTupleSession(rec.UID), Emit: prop.EmitTrue},
			"State":       {Value: "active", Emit: prop.EmitTrue},
			"Sessions":    {Value: m.userSessions(rec.UID), Emit: prop.EmitTrue},
			"IdleHint":    {Value: false, Emit: prop.EmitTrue},
			"Linger":      {Value: false, Emit: prop.EmitConst},
		},
	}
	props, err := prop.Export(m.conn, path, propsSpec)
	if err != nil {
		return
	}
	m.exportPropsWrapper(path, props, userIface)
	_ = m.conn.Export(uo, path, userIface)

	node := &introspect.Node{
		Name: string(path),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name:       userIface,
				Methods:    introspect.Methods(uo),
				Properties: props.Introspection(userIface),
			},
		},
	}
	_ = m.conn.Export(introspect.NewIntrospectable(node), path,
		"org.freedesktop.DBus.Introspectable")
	_ = m.conn.Emit(dbus.ObjectPath(objPath),
		iface+".UserNew", rec.UID, path)
}

// unregisterUserObject.
func (m *manager) unregisterUserObject(uid uint32) {
	path := userPath(uid)
	_ = m.conn.Export(nil, path, userIface)
	_ = m.conn.Export(nil, path, "org.freedesktop.DBus.Properties")
	_ = m.conn.Export(nil, path, "org.freedesktop.DBus.Introspectable")
	_ = m.conn.Emit(dbus.ObjectPath(objPath),
		iface+".UserRemoved", uid, path)
}

// ensureSeat idempotently exports a seat object. seat0 is registered
// at daemon startup regardless of udev because loginctl expects it
// to always be present (dinit's convention, systemd's too). Also
// writes /run/slinit-logind/seats/<id>.json so Manager.ListSeats
// finds it — without the file on disk `loginctl list-seats` reports
// "No seats." even though the object is queryable directly.
//
// libelogind compat: /run/systemd/seats/<id> must exist at daemon
// startup because gdm probes it before creating any greeter session
// (`openat("/run/systemd/seats/seat0") -> ENOENT` was the culprit
// keeping GDM's greeter from ever starting on Sunlight). Written
// with just the seat id — sessions get added by writeCompatSession
// as they come and go.
func (m *manager) ensureSeat(id string) {
	seatRec := struct {
		ID string `json:"id"`
	}{ID: id}
	_ = writeJSONAtomic(filepath.Join(stateRoot, "seats", id+".json"), seatRec)

	// libelogind sd_get_seats + gdm/gnome-shell probing both hit
	// /run/systemd/seats/ at their startup, so seed the compat file
	// unconditionally alongside our JSON. Fields match elogind's own
	// on-disk format (checked against elogind source) — gdm won't
	// spawn a greeter if CAN_GRAPHICAL / CAN_TTY are missing.
	//
	// Goes through the shared writer so a seat seeded here and a seat
	// rewritten after a session change carry the same field set; an
	// earlier split between the two dropped the CAN_* flags as soon as
	// the first session appeared.
	_ = os.MkdirAll("/run/systemd/seats", 0755)
	rewriteCompatAggregates()

	so := &seatObject{m: m, id: id}
	path := seatPath(id)

	propsSpec := map[string]map[string]*prop.Prop{
		seatIface: {
			"Id":            {Value: id, Emit: prop.EmitConst},
			"ActiveSession": {Value: seatActiveTuple(""), Emit: prop.EmitTrue},
			"CanTTY":        {Value: true, Emit: prop.EmitConst},
			"CanGraphical":  {Value: true, Emit: prop.EmitConst},
			"Sessions":      {Value: m.seatSessions(id), Emit: prop.EmitTrue},
			"IdleHint":      {Value: false, Emit: prop.EmitTrue},
		},
	}
	props, err := prop.Export(m.conn, path, propsSpec)
	if err != nil {
		return
	}
	m.exportPropsWrapper(path, props, seatIface)
	_ = m.conn.Export(so, path, seatIface)

	node := &introspect.Node{
		Name: string(path),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name:       seatIface,
				Methods:    introspect.Methods(so),
				Properties: props.Introspection(seatIface),
			},
		},
	}
	_ = m.conn.Export(introspect.NewIntrospectable(node), path,
		"org.freedesktop.DBus.Introspectable")
	_ = m.conn.Emit(dbus.ObjectPath(objPath),
		iface+".SeatNew", id, path)
}

// registerManagerIntrospection publishes the Introspectable interface
// on /org/freedesktop/login1 so `gdbus introspect` (and every desktop
// stack that runs a discovery pass before calling methods) sees the
// full Manager surface. Without it XFCE session integration, gvfs,
// xdg-desktop-portal etc. treat the daemon as absent and downgrade
// (broken right-click menus, no power controls, etc.).
func (m *manager) registerManagerIntrospection() {
	// Property dict (managerprops.go). Exported on the same path under
	// org.freedesktop.DBus.Properties — nothing else claims that
	// interface here, so there's no handler to replace the way the
	// per-object registrars do with propsWrapper.
	_ = m.conn.Export(&managerProperties{m: m}, dbus.ObjectPath(objPath),
		"org.freedesktop.DBus.Properties")

	// Use introspect.Methods to auto-generate the method table from
	// the manager receiver's exported methods. The interface XML is
	// then attached to a Node hosting only Manager — matches what
	// elogind serves on this path.
	node := &introspect.Node{
		Name: objPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name:       iface,
				Properties: managerIntrospectProps(),
				Methods:    introspect.Methods(m),
				Signals: []introspect.Signal{
					{Name: "SessionNew", Args: []introspect.Arg{
						{Name: "session_id", Type: "s", Direction: "out"},
						{Name: "object_path", Type: "o", Direction: "out"},
					}},
					{Name: "SessionRemoved", Args: []introspect.Arg{
						{Name: "session_id", Type: "s", Direction: "out"},
						{Name: "object_path", Type: "o", Direction: "out"},
					}},
					{Name: "UserNew", Args: []introspect.Arg{
						{Name: "uid", Type: "u", Direction: "out"},
						{Name: "object_path", Type: "o", Direction: "out"},
					}},
					{Name: "UserRemoved", Args: []introspect.Arg{
						{Name: "uid", Type: "u", Direction: "out"},
						{Name: "object_path", Type: "o", Direction: "out"},
					}},
					{Name: "SeatNew", Args: []introspect.Arg{
						{Name: "seat_id", Type: "s", Direction: "out"},
						{Name: "object_path", Type: "o", Direction: "out"},
					}},
				},
			},
		},
	}
	_ = m.conn.Export(introspect.NewIntrospectable(node),
		dbus.ObjectPath(objPath),
		"org.freedesktop.DBus.Introspectable")
}

// rehydrateObjects re-exports per-Session / User objects for the
// state that already exists on disk when the daemon starts. Without
// this a slinit-logind restart during an active login leaves the
// session data queryable via `slinitctl show-session` but 404 on any
// D-Bus lookup — pam_close_session's ReleaseSession would then get a
// NoSuchSession spam despite the file being present.
func (m *manager) rehydrateObjects() {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	for _, f := range files {
		var rec SessionRecord
		if readJSON(f, &rec) {
			m.registerSessionObject(rec)
		}
	}
	ufiles, _ := filepath.Glob(filepath.Join(stateRoot, "users", "*.json"))
	for _, f := range ufiles {
		var rec UserRecord
		if readJSON(f, &rec) {
			m.registerUserObject(rec)
		}
	}
}

// --- helpers: composite property values in systemd's shape ---

// userTuple returns the (uid, object_path) struct systemd uses for
// Session.User. Property signature is (uo).
func userTuple(uid uint32) UserRef {
	return UserRef{UID: uid, Path: userPath(uid)}
}

// seatTuple returns the (seat_id, object_path) struct for Session.Seat.
func seatTuple(id string) SeatRef {
	if id == "" {
		return SeatRef{ID: "", Path: dbus.ObjectPath("/")}
	}
	return SeatRef{ID: id, Path: seatPath(id)}
}

// seatActiveTuple returns the (session_id, object_path) tuple for
// Seat.ActiveSession — empty when no session is active.
func seatActiveTuple(sessionID string) SessionRef {
	if sessionID == "" {
		return SessionRef{ID: "", Path: dbus.ObjectPath("/")}
	}
	return SessionRef{ID: sessionID, Path: sessionPath(sessionID)}
}

// userTupleSession returns the tuple systemd exposes as User.Display —
// (session_id, object_path). We pick the first session owned by the
// uid or the empty tuple.
func userTupleSession(uid uint32) SessionRef {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	for _, f := range files {
		var rec SessionRecord
		if readJSON(f, &rec) && rec.UserID == uid {
			return SessionRef{ID: rec.ID, Path: sessionPath(rec.ID)}
		}
	}
	return SessionRef{ID: "", Path: dbus.ObjectPath("/")}
}

// userSessions returns a slice of (session_id, object_path) tuples for
// User.Sessions.
func (m *manager) userSessions(uid uint32) []SessionRef {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	out := []SessionRef{}
	for _, f := range files {
		var rec SessionRecord
		if readJSON(f, &rec) && rec.UserID == uid {
			out = append(out, SessionRef{ID: rec.ID, Path: sessionPath(rec.ID)})
		}
	}
	return out
}

// seatSessions returns the tuple list for Seat.Sessions.
func (m *manager) seatSessions(seatID string) []SessionRef {
	files, _ := filepath.Glob(filepath.Join(stateRoot, "sessions", "*.json"))
	out := []SessionRef{}
	for _, f := range files {
		var rec SessionRecord
		if readJSON(f, &rec) && rec.SeatID == seatID {
			out = append(out, SessionRef{ID: rec.ID, Path: sessionPath(rec.ID)})
		}
	}
	return out
}

// --- per-Session methods (routed to Manager equivalents) ---

func (s *sessionObject) Terminate() *dbus.Error {
	return s.m.ReleaseSession(s.id)
}
func (s *sessionObject) Activate() *dbus.Error {
	return s.m.ActivateSession(s.id)
}
func (s *sessionObject) Lock() *dbus.Error   { return s.m.LockSession(s.id) }
func (s *sessionObject) Unlock() *dbus.Error { return s.m.UnlockSession(s.id) }
func (s *sessionObject) Kill(who string, sig int32) *dbus.Error {
	return s.m.KillSession(s.id, who, sig)
}
func (s *sessionObject) SetIdleHint(idle bool) *dbus.Error {
	// Storage hook lives in Phase C+; the property is Writable so
	// prop.Set landed already handles the change signal.
	return nil
}
// SetLockedHint records that the session's own screen lock is engaged.
// gnome-shell calls it on both edges of a lock. Storing it is what
// makes `loginctl show-session -p LockedHint` tell the truth — it used
// to answer "no" while the screen was plainly locked — and the
// PropertiesChanged that prop.Properties emits on the way is what lets
// a watcher notice without polling.
//
// Distinct from the Lock/Unlock signals: those are a request aimed at
// whoever owns the screen, this is that owner reporting back.
func (s *sessionObject) SetLockedHint(locked bool) *dbus.Error {
	s.m.setSessionProp(s.id, "LockedHint", locked)
	return nil
}

// TakeControl / ReleaseControl are the compositor entry points —
// a Wayland compositor claims the seat before asking for any device,
// and releasing drops every descriptor it was handed. See device.go.
func (s *sessionObject) TakeControl(force bool) *dbus.Error {
	return takeControl(s.id, force)
}

func (s *sessionObject) ReleaseControl() *dbus.Error {
	releaseControl(s.id)
	return nil
}

// TakeDevice hands the caller an fd for the requested device
// major:minor. Wayland compositors (mutter for GDM's greeter and any
// gnome-shell session) call this for /dev/dri/card0 + input devices.
// DRM master is claimed on the way out — without it a compositor holds
// a descriptor it cannot modeset through. See device.go.
func (s *sessionObject) TakeDevice(major, minor uint32) (dbus.UnixFD, bool, *dbus.Error) {
	return takeDevice(s.id, major, minor)
}

func (s *sessionObject) ReleaseDevice(major, minor uint32) *dbus.Error {
	releaseDevice(s.id, major, minor)
	return nil
}

// PauseDeviceComplete is the client's acknowledgement of a PauseDevice
// signal. We don't pause devices — that needs the VT-switch tracking
// elogind does in logind-session-device.c — so there is nothing to
// confirm, but the method has to exist or a compositor that acks
// unconditionally takes an UnknownMethod error mid-handover.
func (s *sessionObject) PauseDeviceComplete(major, minor uint32) *dbus.Error { return nil }

// --- per-User methods ---

func (u *userObject) Terminate() *dbus.Error {
	// Terminate every session owned by this UID. Iterate over a
	// snapshot because ReleaseSession mutates the session file set.
	for _, ref := range u.m.userSessions(u.uid) {
		_ = u.m.ReleaseSession(ref.ID)
	}
	return nil
}
func (u *userObject) Kill(sig int32) *dbus.Error {
	for _, ref := range u.m.userSessions(u.uid) {
		_ = u.m.KillSession(ref.ID, "all", sig)
	}
	return nil
}

// --- per-Seat methods ---

func (s *seatObject) ActivateSession(sessionID string) *dbus.Error {
	return s.m.ActivateSession(sessionID)
}
func (s *seatObject) Terminate() *dbus.Error {
	for _, ref := range s.m.seatSessions(s.id) {
		_ = s.m.ReleaseSession(ref.ID)
	}
	return nil
}
func (s *seatObject) SwitchTo(vtnr uint32) *dbus.Error {
	// VT_ACTIVATE lands in Phase C+.
	return nil
}
func (s *seatObject) SwitchToNext() *dbus.Error     { return nil }
func (s *seatObject) SwitchToPrevious() *dbus.Error { return nil }

// _ pins the fmt+strconv imports needed by other files in the package
// (helpers reach them transitively); explicit to keep this file
// compilable in isolation when session.go/main.go get restructured.
var (
	_ = fmt.Sprintf
	_ = strconv.Itoa
)
