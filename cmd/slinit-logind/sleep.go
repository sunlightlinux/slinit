package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/godbus/dbus/v5"
)

// The sleep handshake.
//
// Writing /sys/power/state is one line; doing it safely is this file. A
// laptop that suspends without telling anyone wakes with an unlocked
// desktop, because the screen locker never heard it was going to sleep.
// Every locker — gnome-screensaver, xfce4-screensaver, the swaylock
// wrappers — waits for PrepareForSleep(true) and locks in response. So
// the order is fixed:
//
//  1. refuse outright if a block lock covers "sleep";
//  2. announce PrepareForSleep(true), which is what makes lockers lock;
//  3. wait for delay locks to clear, bounded by inhibitDelayMax;
//  4. ask PID 1 to do it, so the sleep hook brackets the kernel write in
//     the one place that write happens;
//  5. announce PrepareForSleep(false) once the kernel has returned,
//     which for suspend-to-RAM is after the machine has woken.
//
// Step 4 is a deliberate divergence from writing sysfs here. There were
// two independent writers of /sys/power/state — this daemon and
// `slinitctl suspend` through PID 1 — and neither knew about the other,
// so which door you came through decided whether anything ran. Routing
// through PID 1 leaves one.

// sleepVerb names the slinitctl state token for each login1 method, so
// the error messages and the hook argument agree with what was asked
// for.
type sleepVerb struct {
	method string // the D-Bus method name, for error text
	state  string // the kernel token passed to slinitctl suspend
}

// sleep runs the handshake for one sleep operation.
func (m *manager) sleep(v sleepVerb) *dbus.Error {
	if in := m.inhibitors.blockedBy("sleep"); in != nil {
		return dbus.NewError("org.freedesktop.login1.Error.OperationInhibited",
			[]interface{}{fmt.Sprintf(
				"%s is inhibited by %q (pid %d, uid %d): %s",
				v.method, in.Who, in.PID, in.UID, in.Why)})
	}

	m.announceSleep(true)
	if m.inhibitors.waitForDelays("sleep", inhibitDelayMax) && m.debug {
		// Proceeding anyway is the right call: a locker that crashed
		// holding a delay lock must not keep a lid-shut laptop awake.
		fmt.Fprintf(os.Stderr, "slinit-logind: delay inhibitors still held after %s; "+
			"proceeding with %s\n", inhibitDelayMax, v.method)
	}

	err := suspendViaInit(v.state)

	// Always announced, even when the request failed, so a locker that
	// locked on the way in is not left believing a sleep is still
	// pending.
	m.announceSleep(false)
	return err
}

// announceSleep flips the PreparingForSleep property and emits the
// PrepareForSleep signal clients actually listen for. Both, because
// loginctl reads the property while lockers subscribe to the signal.
//
// PropertiesChanged is emitted by hand: the Manager's properties are
// served by managerProperties reading live getters, not by a
// prop.Properties table, so nothing emits the change for us.
func (m *manager) announceSleep(preparing bool) {
	m.mu.Lock()
	m.preparingForSleep = preparing
	m.mu.Unlock()

	// Both emits are best-effort — a client that missed the signal
	// still sees the property — so a daemon with no bus yet simply
	// announces nothing rather than failing the sleep.
	if m.conn == nil {
		return
	}
	_ = m.conn.Emit(dbus.ObjectPath(objPath), iface+".PrepareForSleep", preparing)
	_ = m.conn.Emit(dbus.ObjectPath(objPath),
		"org.freedesktop.DBus.Properties.PropertiesChanged",
		iface,
		map[string]dbus.Variant{"PreparingForSleep": dbus.MakeVariant(preparing)},
		[]string{})
}

// preparingForSleepNow reports the flag for the property getter.
func (m *manager) preparingForSleepNow() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.preparingForSleep
}

// suspendViaInit asks PID 1 to perform the sleep, and waits: the call
// returns when the kernel has come back, which is what makes it correct
// to announce the end of sleep afterwards.
//
// `slinitctl suspend STATE` is the existing door to CmdSuspend, and
// using it keeps this daemon free of the control-protocol wire format —
// the same reason the power methods exec slinit-shutdown rather than
// speaking the protocol themselves.
func suspendViaInit(state string) *dbus.Error {
	bin := slinitctlSuspendPath
	if bin == "" {
		p, err := exec.LookPath("slinitctl")
		if err != nil {
			return dbus.NewError("org.freedesktop.login1.Error.SleepNotSupported",
				[]interface{}{"slinitctl not found, cannot reach PID 1: " + err.Error()})
		}
		bin = p
	}

	out, err := exec.Command(bin, "--system", "suspend", state).CombinedOutput()
	if err != nil {
		return dbus.NewError("org.freedesktop.login1.Error.SleepNotSupported",
			[]interface{}{fmt.Sprintf("slinitctl suspend %s: %v: %s",
				state, err, trimOutput(out))})
	}
	return nil
}

// trimOutput keeps an error message to one readable line.
func trimOutput(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			s = s[:i] + " " + s[i+1:]
		}
	}
	return s
}

// slinitctlSuspendPath is the control CLI the sleep path execs.
// Overridable so a test can point at a stub; unset in production for
// exec.LookPath discovery, matching slinitctlPath above.
var slinitctlSuspendPath = ""
