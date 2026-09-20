// The self/auto session aliases.
//
// systemd and elogind answer on two magic object paths in addition to
// the per-session ones:
//
//	/org/freedesktop/login1/session/self — the caller's own session
//	/org/freedesktop/login1/session/auto — the caller's session if it
//	  has one, otherwise that user's display session
//
// They exist because a process cannot always name its own session.
// gnome-shell is the case that forced this: its greeter runs without
// XDG_SESSION_ID in the environment (gnome-session-binary has it, the
// shell it spawns does not), so loginManager.js falls back to
// constructing a proxy on .../session/auto and reading Id off it.
// With no object there the proxy still builds, Id comes back undefined,
// and the subsequent GetSession(null) fails with "Argument string may
// not be null" — which is what left GDM showing a bare compositor with
// no login dialog, no menus and no titles.
//
// Unlike the per-session objects these cannot be a static export: the
// answer depends on who is asking. godbus injects the caller's bus name
// into any exported method whose first parameter is a dbus.Sender, so
// both the method and the property handlers resolve at call time.
package main

import (
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// resolveAlias maps "self" or "auto" to a concrete session record for
// the calling process. Any other name is looked up verbatim.
//
// "auto" widens the search the way elogind does: if the caller is not
// itself inside a session, fall back to the display session of the
// caller's user. That is the branch gnome-shell's greeter depends on.
func (m *manager) resolveAlias(sender dbus.Sender, name string) (SessionRecord, bool) {
	switch name {
	case "self", "auto":
	default:
		var rec SessionRecord
		return rec, readJSON(sessionFile(name), &rec)
	}

	pid, err := m.callerPID(string(sender))
	if err != nil {
		return SessionRecord{}, false
	}

	// The caller's own session, by cgroup then by leader pid.
	if id := findSessionByCgroup(pid); id != "" {
		var rec SessionRecord
		if readJSON(sessionFile(id), &rec) {
			return rec, true
		}
	}
	if id, rec := findSessionByLeaderLocked(pid); id != "" && rec != nil {
		return *rec, true
	}

	if name == "self" {
		return SessionRecord{}, false
	}

	// "auto": fall back to the display session of the caller's user.
	uid, ok := procUID(pid)
	if !ok {
		return SessionRecord{}, false
	}
	return displaySessionForUID(uid)
}

// displaySessionForUID picks the user's graphical session, preferring
// wayland/x11 over a tty one — the same preference sd_uid_get_display
// encodes in the DISPLAY= field of /run/systemd/users/<uid>.
func displaySessionForUID(uid uint32) (SessionRecord, bool) {
	var fallback SessionRecord
	var haveFallback bool
	for _, rec := range loadSessionRecords() {
		if rec.UserID != uid {
			continue
		}
		if rec.Type == "wayland" || rec.Type == "x11" {
			return rec, true
		}
		if !haveFallback {
			fallback, haveFallback = rec, true
		}
	}
	return fallback, haveFallback
}

// sessionAlias serves one of the magic paths. It carries no session id
// of its own — every call resolves against the sender.
type sessionAlias struct {
	m    *manager
	name string // "self" or "auto"
}

func (a *sessionAlias) resolve(sender dbus.Sender) (*sessionObject, *dbus.Error) {
	rec, ok := a.m.resolveAlias(sender, a.name)
	if !ok {
		return nil, dbus.NewError("org.freedesktop.login1.Error.NoSessionForPID",
			[]any{"caller is not part of any session"})
	}
	return &sessionObject{m: a.m, id: rec.ID}, nil
}

func (a *sessionAlias) Terminate(sender dbus.Sender) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.Terminate()
}

func (a *sessionAlias) Activate(sender dbus.Sender) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.Activate()
}

func (a *sessionAlias) Lock(sender dbus.Sender) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.Lock()
}

func (a *sessionAlias) Unlock(sender dbus.Sender) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.Unlock()
}

func (a *sessionAlias) SetIdleHint(sender dbus.Sender, idle bool) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.SetIdleHint(idle)
}

func (a *sessionAlias) SetLockedHint(sender dbus.Sender, locked bool) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.SetLockedHint(locked)
}

func (a *sessionAlias) Kill(sender dbus.Sender, who string, sig int32) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.Kill(who, sig)
}

func (a *sessionAlias) TakeControl(sender dbus.Sender, force bool) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.TakeControl(force)
}

func (a *sessionAlias) ReleaseControl(sender dbus.Sender) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.ReleaseControl()
}

func (a *sessionAlias) TakeDevice(sender dbus.Sender, major, minor uint32) (dbus.UnixFD, bool, *dbus.Error) {
	so, err := a.resolve(sender)
	if err != nil {
		return 0, false, err
	}
	return so.TakeDevice(major, minor)
}

func (a *sessionAlias) ReleaseDevice(sender dbus.Sender, major, minor uint32) *dbus.Error {
	so, err := a.resolve(sender)
	if err != nil {
		return err
	}
	return so.ReleaseDevice(major, minor)
}

func (a *sessionAlias) PauseDeviceComplete(sender dbus.Sender, major, minor uint32) *dbus.Error {
	return nil
}

// aliasProps serves org.freedesktop.DBus.Properties on an alias path,
// resolving the session per caller. Mirrors propsWrapper's tolerance of
// an empty interface name, which is how loginctl enumerates.
type aliasProps struct{ a *sessionAlias }

func (p *aliasProps) values(sender dbus.Sender) (map[string]dbus.Variant, *dbus.Error) {
	rec, ok := p.a.m.resolveAlias(sender, p.a.name)
	if !ok {
		return nil, dbus.NewError("org.freedesktop.login1.Error.NoSessionForPID",
			[]any{"caller is not part of any session"})
	}
	out := map[string]dbus.Variant{}
	for name, val := range sessionPropValues(rec) {
		out[name] = dbus.MakeVariant(val)
	}
	return out, nil
}

func (p *aliasProps) Get(sender dbus.Sender, ifaceName, name string) (dbus.Variant, *dbus.Error) {
	if ifaceName != "" && ifaceName != sessionIface {
		return dbus.Variant{}, prop.ErrIfaceNotFound
	}
	vals, err := p.values(sender)
	if err != nil {
		return dbus.Variant{}, err
	}
	v, ok := vals[name]
	if !ok {
		return dbus.Variant{}, prop.ErrPropNotFound
	}
	return v, nil
}

func (p *aliasProps) GetAll(sender dbus.Sender, ifaceName string) (map[string]dbus.Variant, *dbus.Error) {
	if ifaceName != "" && ifaceName != sessionIface {
		return nil, prop.ErrIfaceNotFound
	}
	return p.values(sender)
}

func (p *aliasProps) Set(sender dbus.Sender, ifaceName, name string, v dbus.Variant) *dbus.Error {
	if ifaceName != "" && ifaceName != sessionIface {
		return prop.ErrIfaceNotFound
	}
	if !sessionWritableProps[name] {
		return prop.ErrReadOnly
	}
	// IdleHint / LockedHint aren't stored yet (see the Session object's
	// setters), so accept and drop rather than fail a caller that is
	// just announcing its state.
	return nil
}

// registerSessionAliases exports the self and auto paths. Called once
// at startup — they are not tied to any session's lifetime.
func (m *manager) registerSessionAliases() {
	for _, name := range []string{"self", "auto"} {
		a := &sessionAlias{m: m, name: name}
		path := dbus.ObjectPath(objPath + "/session/" + name)

		_ = m.conn.Export(a, path, sessionIface)
		_ = m.conn.Export(&aliasProps{a: a}, path, "org.freedesktop.DBus.Properties")

		// Introspection is built from a throwaway sessionObject so the
		// method list matches the real per-session objects; the alias
		// receiver only differs by the injected sender argument, which
		// godbus strips before it reaches the wire signature.
		props := make([]introspect.Property, 0, len(sessionPropValues(SessionRecord{})))
		for propName := range sessionPropValues(SessionRecord{}) {
			access := "read"
			if sessionWritableProps[propName] {
				access = "readwrite"
			}
			props = append(props, introspect.Property{
				Name:   propName,
				Type:   dbus.SignatureOf(sessionPropValues(SessionRecord{})[propName]).String(),
				Access: access,
			})
		}
		node := &introspect.Node{
			Name: string(path),
			Interfaces: []introspect.Interface{
				introspect.IntrospectData,
				prop.IntrospectData,
				{
					Name:       sessionIface,
					Methods:    introspect.Methods(&sessionObject{m: m}),
					Properties: props,
				},
			},
		}
		_ = m.conn.Export(introspect.NewIntrospectable(node), path,
			"org.freedesktop.DBus.Introspectable")
	}
}
