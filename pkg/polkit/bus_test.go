package polkit

import (
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/sunlightlinux/slinit/internal/dbustest"
)

// fakeBus answers the bus daemon's GetConnectionUnixUser.
type fakeBus struct{ uids map[string]uint32 }

func (b *fakeBus) GetConnectionUnixUser(name string) (uint32, *dbus.Error) {
	uid, ok := b.uids[name]
	if !ok {
		return 0, dbus.NewError("org.freedesktop.DBus.Error.NameHasNoOwner", []any{name})
	}
	return uid, nil
}

type pkSubject struct {
	Kind    string
	Details map[string]dbus.Variant
}

type pkResult struct {
	Authorized bool
	Challenge  bool
	Details    map[string]string
}

// fakePolkit is polkitd's Authority: it records each CheckAuthorization
// and answers from a table keyed by action.
type fakePolkit struct {
	mu      sync.Mutex
	calls   []pkCall
	answers map[string]pkResult
}

type pkCall struct {
	subject pkSubject
	action  string
	details map[string]string
	flags   uint32
	cancel  string
}

func (p *fakePolkit) CheckAuthorization(subject pkSubject, action string, details map[string]string,
	flags uint32, cancel string) (pkResult, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, pkCall{subject, action, details, flags, cancel})
	r := p.answers[action]
	if r.Details == nil {
		r.Details = map[string]string{}
	}
	return r, nil
}

// recorded returns a copy of the calls so far. The fake runs on the bus
// connection's goroutine, and the race detector cannot see the ordering
// the socket gives, so the test reads under the lock.
func (p *fakePolkit) recorded() []pkCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pkCall(nil), p.calls...)
}

// New's own bus calls, over a stand-in bus: the uid comes from the bus
// daemon, and polkitd gets the caller as a system-bus-name subject, the
// details, and the interaction flag only when the caller allowed it.
// Until now only Authorize's decision table was tested, with both calls
// replaced.
func TestNewAsksTheBusAndPolkitd(t *testing.T) {
	server, client := dbustest.Pair(t)
	bus := &fakeBus{uids: map[string]uint32{":1.10": 1000, ":1.0": 0}}
	pk := &fakePolkit{answers: map[string]pkResult{
		"org.test.allowed":   {Authorized: true},
		"org.test.challenge": {Challenge: true},
		"org.test.refused":   {},
	}}
	if err := server.Export(bus, "/org/freedesktop/DBus", "org.freedesktop.DBus"); err != nil {
		t.Fatal(err)
	}
	if err := server.Export(pk, authorityPath, authorityName+".Authority"); err != nil {
		t.Fatal(err)
	}
	c := New(client)

	if err := c.Authorize(":1.10", "org.test.allowed", map[string]string{"zone": "Europe/Paris"}, true); err != nil {
		t.Fatalf("authorized action refused: %v", err)
	}
	if len(pk.recorded()) != 1 {
		t.Fatalf("polkitd asked %d times, want 1", len(pk.recorded()))
	}
	got := pk.recorded()[0]
	if got.subject.Kind != "system-bus-name" {
		t.Errorf("subject kind %q", got.subject.Kind)
	}
	if name, _ := got.subject.Details["name"].Value().(string); name != ":1.10" {
		t.Errorf("subject name %v, want the caller :1.10", got.subject.Details["name"])
	}
	if got.action != "org.test.allowed" || got.details["zone"] != "Europe/Paris" {
		t.Errorf("action %q details %v", got.action, got.details)
	}
	if got.flags != allowUserInter {
		t.Errorf("interactive call sent flags %d, want %d", got.flags, allowUserInter)
	}

	err := c.Authorize(":1.10", "org.test.challenge", nil, false)
	if err == nil || err.Name != "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired" {
		t.Errorf("challenge without interaction: %v", err)
	}
	calls := pk.recorded()
	if last := calls[len(calls)-1]; last.flags != 0 || last.details == nil {
		t.Errorf("non-interactive call: flags %d details %v (nil details must go as an empty map)",
			last.flags, last.details)
	}
	if err := c.Authorize(":1.10", "org.test.challenge", nil, true); err == nil ||
		err.Name != "org.freedesktop.DBus.Error.AccessDenied" {
		t.Errorf("challenge with interaction, not granted: %v", err)
	}
	if err := c.Authorize(":1.10", "org.test.refused", nil, true); err == nil ||
		err.Name != "org.freedesktop.DBus.Error.AccessDenied" {
		t.Errorf("refused action: %v", err)
	}

	// Root never reaches polkitd.
	n := len(pk.recorded())
	if err := c.Authorize(":1.0", "org.test.refused", nil, false); err != nil {
		t.Errorf("root refused: %v", err)
	}
	if len(pk.recorded()) != n {
		t.Errorf("polkitd was asked about root")
	}

	// A name the bus does not know is refused, not treated as root.
	if err := c.Authorize(":1.99", "org.test.allowed", nil, false); err == nil ||
		err.Name != "org.freedesktop.DBus.Error.AccessDenied" {
		t.Errorf("unknown sender: %v", err)
	}
}

// Without polkitd on the bus a non-root caller is refused: the daemons
// using this act on the whole machine, so they fail closed.
func TestNoPolkitdFailsClosed(t *testing.T) {
	server, client := dbustest.Pair(t)
	bus := &fakeBus{uids: map[string]uint32{":1.10": 1000, ":1.0": 0}}
	if err := server.Export(bus, "/org/freedesktop/DBus", "org.freedesktop.DBus"); err != nil {
		t.Fatal(err)
	}
	c := New(client)
	if err := c.Authorize(":1.10", "org.test.allowed", nil, true); err == nil ||
		err.Name != "org.freedesktop.DBus.Error.AccessDenied" {
		t.Errorf("no polkitd, non-root: %v", err)
	}
	if err := c.Authorize(":1.0", "org.test.allowed", nil, true); err != nil {
		t.Errorf("no polkitd, root: %v", err)
	}

	empty := &Checker{}
	empty.UID, empty.Check = empty.busUID, empty.polkitCheck
	if err := empty.Authorize(":1.0", "org.test.allowed", nil, false); err == nil {
		t.Errorf("no bus connection at all was allowed")
	}
}
