package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/internal/dbustest"
)

// newStub is the systemd1 stub on a connection of its own; uids maps the
// callers the system bus would know, nil for a session bus.
func newStub(t *testing.T, uids map[dbus.Sender]uint32) *systemd1Manager {
	t.Helper()
	server, _ := dbustest.Pair(t)
	s := &systemd1Manager{conn: server, env: map[string]string{}, started: map[string]bool{}}
	if uids != nil {
		s.callerUID = func(sender dbus.Sender) (uint32, error) {
			if uid, ok := uids[sender]; ok {
				return uid, nil
			}
			return 0, errors.New("no such name")
		}
	}
	return s
}

// callStub calls an exported method of the stub by name as sender, with
// zero values for the other arguments, and returns its *dbus.Error.
func callStub(s *systemd1Manager, name string, sender dbus.Sender) *dbus.Error {
	m := reflect.ValueOf(s).MethodByName(name)
	args := make([]reflect.Value, m.Type().NumIn())
	for i := range args {
		if m.Type().In(i) == reflect.TypeOf(dbus.Sender("")) {
			args[i] = reflect.ValueOf(sender)
		} else {
			args[i] = reflect.Zero(m.Type().In(i))
		}
	}
	out := m.Call(args)
	err, _ := out[len(out)-1].Interface().(*dbus.Error)
	return err
}

// On the system bus the stub was open to every local user: SetEnvironment
// and StartUnit kept whatever they were sent and LoadUnit exported an
// object per name, without bound. Every method there is now root's but
// the GetUnitByPID* lookups polkitd makes. Every method is listed by
// reflection, so a method added later is checked too.
func TestSystemd1StubIsRootsOnSystemBus(t *testing.T) {
	s := newStub(t, map[dbus.Sender]uint32{alice: 1000, root: 0})
	typ := reflect.TypeOf(s)
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		err := callStub(s, name, alice)
		if strings.HasPrefix(name, "GetUnitByPID") {
			if denied(err) {
				t.Errorf("%s refused to a user; polkitd needs it", name)
			}
			continue
		}
		if !denied(err) {
			t.Errorf("%s from a user on the system bus: %v, want AccessDenied", name, err)
		}
		if !denied(callStub(s, name, ghost)) {
			t.Errorf("%s from an unknown caller was not refused", name)
		}
	}
	if len(s.env) != 0 || len(s.started) != 0 || len(s.units) != 0 {
		t.Errorf("refused calls left state: env %v, started %v, units %v", s.env, s.started, s.units)
	}
	if err := s.SetEnvironment(root, []string{"A=1"}); err != nil || s.env["A"] != "A=1" {
		t.Errorf("root SetEnvironment: %v, env %v", err, s.env)
	}
	if _, err := s.LoadUnit(root, "x.service"); err != nil {
		t.Errorf("root LoadUnit: %v", err)
	}
}

// On a user's session bus every caller is that user, and nothing is
// refused.
func TestSystemd1StubOpenOnSessionBus(t *testing.T) {
	s := newStub(t, nil)
	if err := s.SetEnvironment(alice, []string{"A=1"}); err != nil {
		t.Errorf("SetEnvironment: %v", err)
	}
	if _, err := s.LoadUnit(alice, "x.service"); err != nil {
		t.Errorf("LoadUnit: %v", err)
	}
}

// The unit objects GetUnit and LoadUnit export are never removed, so
// their number is bounded.
func TestSystemd1UnitObjectsAreBounded(t *testing.T) {
	s := newStub(t, nil)
	for i := 0; i < maxUnitObjects; i++ {
		if _, err := s.LoadUnit(root, fmt.Sprintf("u%d.service", i)); err != nil {
			t.Fatalf("LoadUnit #%d: %v", i, err)
		}
	}
	if _, err := s.LoadUnit(root, "one-more.service"); err == nil || err.Name != "org.freedesktop.DBus.Error.LimitsExceeded" {
		t.Errorf("LoadUnit past the bound: %v, want LimitsExceeded", err)
	}
	if _, err := s.LoadUnit(root, "u0.service"); err != nil {
		t.Errorf("LoadUnit of a name already exported: %v", err)
	}
}
