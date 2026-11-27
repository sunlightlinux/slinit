package main

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/godbus/dbus/v5"
)

// Polkit's defaults for org.freedesktop.login1.inhibit-*: delay locks on
// shutdown and sleep, and idle blocks, are anyone's; block locks on
// shutdown and sleep and the handle-* key locks need a local session,
// foreground or not. Inhibit used to be root's alone, and gnome-session
// logged "Failed to get delay inhibitor: access denied" on every phase.
func TestInhibitFollowsPolkitDefaults(t *testing.T) {
	w := newPowerWorld(t, 2)
	w.session(SessionRecord{ID: "c2", UserID: 1000, SeatID: "seat0", VTNr: 2, TTY: "tty2"})
	w.session(SessionRecord{ID: "c3", UserID: 1001, SeatID: "seat0", VTNr: 3, TTY: "tty3"})
	w.session(SessionRecord{ID: "r1", UserID: 1002, Remote: true})
	desk := w.caller(":1.desk", 1000, 4242, "c2")   // foreground
	bg := w.caller(":1.bg", 1001, 4343, "c3")       // local, background
	remote := w.caller(":1.ssh", 1002, 4444, "r1")  // remote
	nobody := w.caller(":1.daemon", 1003, 4545, "") // in no session
	rootc := w.caller(":1.root", 0, 1, "")

	cases := []struct {
		what, mode string
		allowed    []dbus.Sender
		refused    []dbus.Sender
	}{
		{"sleep", "delay", []dbus.Sender{desk, bg, remote, nobody, rootc}, nil},
		{"shutdown", "delay", []dbus.Sender{desk, bg, remote, nobody, rootc}, nil},
		{"idle", "block", []dbus.Sender{desk, bg, remote, nobody, rootc}, nil},
		{"sleep", "block", []dbus.Sender{desk, bg, rootc}, []dbus.Sender{remote, nobody}},
		{"shutdown", "block", []dbus.Sender{desk, bg, rootc}, []dbus.Sender{remote, nobody}},
		{"handle-power-key:handle-lid-switch", "block", []dbus.Sender{desk, bg, rootc}, []dbus.Sender{remote, nobody}},
		{"handle-suspend-key", "delay", []dbus.Sender{desk, rootc}, []dbus.Sender{remote, nobody}},
		// One class needing a session makes the whole lock need one.
		{"sleep:handle-power-key", "delay", []dbus.Sender{desk, rootc}, []dbus.Sender{remote, nobody}},
	}
	for _, c := range cases {
		for _, s := range c.allowed {
			if err := w.m.mayInhibit(s, c.what, c.mode); err != nil {
				t.Errorf("%s %s from %s: refused: %v", c.what, c.mode, s, err)
			}
		}
		for _, s := range c.refused {
			if !denied(w.m.mayInhibit(s, c.what, c.mode)) {
				t.Errorf("%s %s from %s: allowed, want AccessDenied", c.what, c.mode, s)
			}
		}
	}
	if !denied(w.m.mayInhibit(":1.gone", "sleep", "delay")) {
		t.Error("an unidentifiable caller must be refused")
	}

	// Through the D-Bus entry point, so a method that skips the rule fails.
	fd, err := w.m.Inhibit(remote, "sleep", "screen locker", "lock first", "delay")
	if err != nil {
		t.Fatalf("Inhibit delay from a user: %v", err)
	}
	// fd is the reply's own descriptor, which replyFD closes by number
	// after fdSendGrace; closing it here as well let that timer close
	// whatever reused the number two seconds later - the pipe of a
	// later test's inhibitor, whose release then never came.
	_ = fd
	if _, err := w.m.Inhibit(remote, "shutdown", "x", "y", "block"); !denied(err) {
		t.Errorf("Inhibit block from a remote user: got %v, want AccessDenied", err)
	}
}

// Anyone may take a delay lock, so the number held is capped, as
// systemd's InhibitorsMax does, or one user could exhaust logind's fds.
func TestInhibitorsAreCapped(t *testing.T) {
	old := inhibitorsMax
	inhibitorsMax = 3
	t.Cleanup(func() { inhibitorsMax = old })
	r := newInhibitRegistry()
	for i := 0; i < 3; i++ {
		c, _, err := r.add("sleep", "w", "y", "delay", 1000, 1)
		if err != nil {
			t.Fatalf("lock %d: %v", i, err)
		}
		t.Cleanup(func() { c.Close() })
	}
	if _, _, err := r.add("sleep", "w", "y", "delay", 1000, 1); !errors.Is(err, errTooManyInhibitors) {
		t.Fatalf("lock past the cap: err = %v", err)
	}

	m := &manager{inhibitors: r}
	fakeBus(t, map[dbus.Sender]uint32{":1.u": 1000})
	if _, err := m.Inhibit(":1.u", "idle", "w", "y", "block"); err == nil || err.Name != "org.freedesktop.DBus.Error.LimitsExceeded" {
		t.Errorf("Inhibit past the cap: got %v, want LimitsExceeded", err)
	}
}

// Inhibit's strings are bounded: a lock lasts as long as its holder
// wants, and anyone may take a delay lock.
func TestInhibitRefusesLongStrings(t *testing.T) {
	w := newPowerWorld(t, 2)
	rootc := w.caller(":1.root", 0, 1, "")
	long := strings.Repeat("x", inhibitStringMax+1)
	for _, args := range [][4]string{
		{"sleep", long, "why", "delay"},
		{"sleep", "who", long, "delay"},
		{long, "who", "why", "delay"},
		{"sleep", "who", "why", long},
	} {
		_, err := w.m.Inhibit(rootc, args[0], args[1], args[2], args[3])
		if err == nil || err.Name != "org.freedesktop.DBus.Error.InvalidArgs" {
			t.Errorf("Inhibit with a %d-byte argument: %v, want InvalidArgs", len(long), err)
		}
	}
	if n := len(w.m.inhibitors.list()); n != 0 {
		t.Errorf("%d locks taken", n)
	}
	if _, err := w.m.Inhibit(rootc, "sleep", strings.Repeat("x", inhibitStringMax), "why", "delay"); err != nil {
		t.Errorf("Inhibit at the bound: %v", err)
	}
}

// InhibitorsMax is systemd's 8192, but systemd runs logind with
// LimitNOFILE=524288; slinit-logind gets the kernel's 4096. Each lock
// holds a descriptor, so a user taking delay locks ran logind out of
// descriptors around lock 4090, long before the cap, and from then on
// it could open nothing: no new session, no device for a seat. The cap
// is lowered to a quarter of the descriptor limit.
func TestInhibitorsCapLeavesDescriptors(t *testing.T) {
	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &orig); err != nil {
		t.Fatal(err)
	}
	// Sized from what the process already has open: earlier tests'
	// inhibitor pipes are closed asynchronously, by goroutines that may
	// lag under load, and with an absolute 512 the registry ran out of
	// descriptors before reaching its cap. The cap is a quarter of the
	// limit and a lock holds two descriptors here, so the limit must be
	// at least twice what is already open for the cap to be reachable;
	// in logind that baseline is a handful of descriptors.
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd:", err)
	}
	lowered := orig
	lowered.Cur = 2*uint64(len(ents)) + 512
	if lowered.Cur > orig.Cur {
		t.Skip("soft NOFILE limit too low to test the cap")
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lowered); err != nil {
		t.Skip("cannot lower RLIMIT_NOFILE:", err)
	}
	t.Cleanup(func() { syscall.Setrlimit(syscall.RLIMIT_NOFILE, &orig) })
	old := inhibitorsMax
	t.Cleanup(func() { inhibitorsMax = old })

	capInhibitorsToFDs()

	r := newInhibitRegistry()
	for i := 0; i < old; i++ {
		var c *os.File
		if c, _, err = r.add("sleep", "w", "y", "delay", 1000, 1); err != nil {
			break
		}
		t.Cleanup(func() { c.Close() })
	}
	if !errors.Is(err, errTooManyInhibitors) {
		t.Fatalf("filling the registry under a %d-fd limit ended with %v, want the cap", lowered.Cur, err)
	}
	f, err := os.Open("/proc/self/status")
	if err != nil {
		t.Fatalf("at the inhibitor cap logind cannot open a file: %v", err)
	}
	f.Close()
}
