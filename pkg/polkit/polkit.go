// Package polkit asks polkitd whether a D-Bus caller may perform an action,
// the way systemd's daemons do (bus_verify_polkit_async). Sunlight runs
// polkitd and GNOME Shell carries an authentication agent, so a request
// marked interactive can prompt for an administrator's password.
//
// Without polkitd on the bus the answer is root's alone: the daemons that
// use this act on the whole machine, and failing open there is not an
// option.
package polkit

import (
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	authorityName  = "org.freedesktop.PolicyKit1"
	authorityPath  = "/org/freedesktop/PolicyKit1/Authority"
	checkMethod    = authorityName + ".Authority.CheckAuthorization"
	allowUserInter = 1 // CheckAuthorizationFlags.AllowUserInteraction
)

// ErrAccessDenied is the D-Bus error a refused caller gets.
func ErrAccessDenied(action string) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.AccessDenied",
		[]any{fmt.Sprintf("access denied for %s", action)})
}

// ErrInteractiveAuthRequired is what systemd answers when the action needs
// authentication and the caller did not allow interaction.
func ErrInteractiveAuthRequired(action string) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.InteractiveAuthorizationRequired",
		[]any{fmt.Sprintf("interactive authentication required for %s", action)})
}

// Checker decides for one bus connection. The function fields are there
// so tests can stand in for the bus daemon and for polkitd.
type Checker struct {
	Conn *dbus.Conn
	// UID returns the uid behind a bus name.
	UID func(sender dbus.Sender) (uint32, error)
	// Check asks polkitd; it returns (authorized, challenge, err).
	Check func(sender dbus.Sender, action string, details map[string]string, interactive bool) (bool, bool, error)
}

// New returns a Checker that talks to the bus daemon and polkitd on conn.
func New(conn *dbus.Conn) *Checker {
	c := &Checker{Conn: conn}
	c.UID = c.busUID
	c.Check = c.polkitCheck
	return c
}

// Authorize returns nil when sender may perform action. Root always may;
// anyone else is decided by polkitd, with details passed along (systemd
// passes e.g. the new time zone). When polkitd cannot be asked, only root
// is allowed.
func (c *Checker) Authorize(sender dbus.Sender, action string, details map[string]string, interactive bool) *dbus.Error {
	uid, err := c.UID(sender)
	if err != nil {
		return ErrAccessDenied(action)
	}
	if uid == 0 {
		return nil
	}
	ok, challenge, err := c.Check(sender, action, details, interactive)
	if err != nil {
		return ErrAccessDenied(action)
	}
	if ok {
		return nil
	}
	if challenge && !interactive {
		return ErrInteractiveAuthRequired(action)
	}
	return ErrAccessDenied(action)
}

func (c *Checker) busUID(sender dbus.Sender) (uint32, error) {
	if c.Conn == nil {
		return 0, errors.New("no bus connection")
	}
	var uid uint32
	err := c.Conn.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixUser", 0, string(sender)).Store(&uid)
	return uid, err
}

// polkitCheck calls CheckAuthorization for the subject "system-bus-name",
// which polkitd resolves to the caller's process and session itself. The
// call blocks while an agent prompts; godbus runs each method call in its
// own goroutine, so the daemon keeps answering others meanwhile.
func (c *Checker) polkitCheck(sender dbus.Sender, action string, details map[string]string, interactive bool) (bool, bool, error) {
	if c.Conn == nil {
		return false, false, errors.New("no bus connection")
	}
	subject := struct {
		Kind    string
		Details map[string]dbus.Variant
	}{"system-bus-name", map[string]dbus.Variant{"name": dbus.MakeVariant(string(sender))}}
	if details == nil {
		details = map[string]string{}
	}
	var flags uint32
	if interactive {
		flags = allowUserInter
	}
	var result struct {
		Authorized bool
		Challenge  bool
		Details    map[string]string
	}
	err := c.Conn.Object(authorityName, authorityPath).
		Call(checkMethod, 0, subject, action, details, flags, "").Store(&result)
	if err != nil {
		return false, false, err
	}
	return result.Authorized, result.Challenge, nil
}
