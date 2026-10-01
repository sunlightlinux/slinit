package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
)

// Suspending through slinit-logind rather than straight at PID 1.
//
// The daemon is what emits PrepareForSleep, which is the signal every
// screen locker waits for. Going around it means the machine can wake
// with an unlocked desktop, so this is the default path and the direct
// one needs --no-coordination.

// logindSuspendTimeout bounds the call. The method does not return until
// the machine has woken, so this has to be long enough for a real sleep
// and short enough that a wedged daemon does not hang the CLI for ever.
// A suspend that has not come back in an hour is not coming back.
const logindSuspendTimeout = time.Hour

// suspendViaLogind asks slinit-logind to perform the sleep.
//
// Reports done=true when the daemon handled it. done=false means the
// request could not be routed that way at all — no system bus, no daemon
// on the name, or a sleep state login1 has no method for — and the caller
// should fall back to the direct path. An error means the daemon was
// reached and refused, which is a real answer and must not be retried
// behind its back: a block inhibitor saying "no" is the whole point.
func suspendViaLogind(state string) (done bool, err error) {
	method, ok := logindSleepMethod(state)
	if !ok {
		// freeze and standby have no login1 method. Nothing is lost by
		// going direct — but say so, because the screen will not lock.
		fmt.Fprintf(os.Stderr, "slinitctl: %q has no login1 method; "+
			"suspending without coordination (no screen lock)\n", state)
		return false, nil
	}

	conn, err := dbus.SystemBus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinitctl: no system bus (%v); "+
			"suspending without coordination (no screen lock)\n", err)
		return false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), logindSuspendTimeout)
	defer cancel()

	obj := conn.Object("org.freedesktop.login1", "/org/freedesktop/login1")
	call := obj.CallWithContext(ctx, "org.freedesktop.login1.Manager."+method, 0, false)
	if call.Err != nil {
		if isLogindAbsent(call.Err) {
			fmt.Fprintf(os.Stderr, "slinitctl: slinit-logind is not running; "+
				"suspending without coordination (no screen lock)\n")
			return false, nil
		}
		// A refusal, typically an inhibitor. Surface it; do not route
		// around the daemon that just said no.
		return false, fmt.Errorf("suspend: %w", call.Err)
	}
	return true, nil
}

// logindSleepMethod maps a kernel sleep state to the login1 method that
// performs it. The two states with no method are suspend-to-idle and
// power-on standby, which login1 simply does not model.
func logindSleepMethod(state string) (string, bool) {
	switch state {
	case "", "mem":
		return "Suspend", true
	case "disk":
		return "Hibernate", true
	default:
		return "", false
	}
}

// isLogindAbsent distinguishes "nobody owns the name" from a refusal by
// the daemon. Only the former justifies going around it.
func isLogindAbsent(err error) bool {
	var derr dbus.Error
	if e, ok := err.(dbus.Error); ok {
		derr = e
	} else if p, ok := err.(*dbus.Error); ok && p != nil {
		derr = *p
	} else {
		return false
	}
	switch derr.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.NoReply":
		return true
	}
	return false
}
