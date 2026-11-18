package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/dbusservice"
	"github.com/sunlightlinux/slinit/pkg/locale"
	"github.com/sunlightlinux/slinit/pkg/polkit"
)

const (
	busName = "org.freedesktop.locale1"
	objPath = dbus.ObjectPath("/org/freedesktop/locale1")
	iface   = "org.freedesktop.locale1"
)

type server struct {
	mu  sync.Mutex
	svc *dbusservice.Service
	pk  *polkit.Checker
	// loadKeymap applies a console keymap now (loadkeys); systemd restarts
	// systemd-vconsole-setup for the same purpose. A variable for tests.
	loadKeymap func(string)
}

func newServer(conn *dbus.Conn, pk *polkit.Checker) *server {
	s := &server{pk: pk, loadKeymap: loadkeys}
	s.svc = &dbusservice.Service{
		Name: "slinit-localed", Conn: conn, Path: objPath, Iface: iface,
		Props: properties, Get: s.property,
	}
	return s
}

func loadkeys(keymap string) {
	if keymap == "" {
		return
	}
	if p, err := exec.LookPath("loadkeys"); err == nil {
		_ = exec.Command(p, "-q", keymap).Run()
	}
}

var properties = []dbusservice.Property{
	{Name: "Locale", Sig: "as", Emits: true},
	{Name: "VConsoleKeymap", Sig: "s", Emits: true},
	{Name: "VConsoleKeymapToggle", Sig: "s", Emits: true},
	{Name: "X11Layout", Sig: "s", Emits: true},
	{Name: "X11Model", Sig: "s", Emits: true},
	{Name: "X11Variant", Sig: "s", Emits: true},
	{Name: "X11Options", Sig: "s", Emits: true},
}

func (s *server) property(name string) (dbus.Variant, bool) {
	switch name {
	case "Locale":
		m, _ := locale.ReadLocale()
		return dbus.MakeVariant(locale.LocaleEnv(m)), true
	case "VConsoleKeymap", "VConsoleKeymapToggle":
		vc, _, _ := locale.ReadVConsole()
		if name == "VConsoleKeymap" {
			return dbus.MakeVariant(vc.Keymap), true
		}
		return dbus.MakeVariant(vc.Toggle), true
	case "X11Layout", "X11Model", "X11Variant", "X11Options":
		x := locale.CurrentX11()
		return dbus.MakeVariant(map[string]string{"X11Layout": x.Layout, "X11Model": x.Model,
			"X11Variant": x.Variant, "X11Options": x.Options}[name]), true
	}
	return dbus.Variant{}, false
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// SetLocale sets the system locale. A single bare locale means LANG;
// otherwise each item is VAR=value. Variables not given keep their value,
// and ones equal to LANG are dropped, as systemd-localed merges them.
//
// Like the keyboard methods, it decides whether anything changes before
// asking polkit (which may wait on a password), then works the change out
// again from the files as they are once authorized, so a change made in
// between is not overwritten with a stale one.
func (s *server) SetLocale(sender dbus.Sender, items []string, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
	if _, change, err := locale.PlanLocale(items); err != nil {
		return planError(err)
	} else if !change {
		return nil
	}
	if err := s.pk.Authorize(sender, iface+".set-locale", nil, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	merged, change, err := locale.PlanLocale(items)
	if err != nil {
		return planError(err)
	}
	if !change {
		return nil
	}
	if err := locale.WriteLocale(merged); err != nil {
		return dbusservice.Failed("Failed to set locale: %v", err)
	}
	if env := locale.LocaleEnv(merged); len(env) == 0 {
		s.svc.Logf("Changed locale to unset.")
	} else {
		s.svc.Logf("Changed locale to %s.", strings.Join(env, ", "))
	}
	s.svc.Emit("Locale")
	return nil
}

// planError maps a planning failure: a bad argument is InvalidArgs, a
// file that cannot be read is Failed.
func planError(err error) *dbus.Error {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return dbusservice.Failed("Failed to read locale data: %v", err)
	}
	return dbusservice.InvalidArgs("%s", err.Error())
}

var x11Props = []string{"X11Layout", "X11Model", "X11Variant", "X11Options"}

// SetVConsoleKeyboard sets the console keymap and its toggle. With
// convert, the X11 layout follows through kbd-model-map when it has a row
// for the keymap.
func (s *server) SetVConsoleKeyboard(sender dbus.Sender, keymap, toggle string, convert, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
	for _, k := range []string{keymap, toggle} {
		if k != "" && !locale.KeymapExists(k) {
			return dbusservice.Failed("Keymap %s is not installed.", k)
		}
	}
	if p, err := locale.PlanVConsole(keymap, toggle, convert); err != nil {
		return dbusservice.Failed("Failed to read virtual console keymap data: %v", err)
	} else if !p.Change {
		return nil
	}
	if err := s.pk.Authorize(sender, iface+".set-keyboard", nil, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := locale.PlanVConsole(keymap, toggle, convert)
	if err != nil {
		return dbusservice.Failed("Failed to read virtual console keymap data: %v", err)
	}
	if !p.Change {
		return nil
	}
	if p.Note != "" {
		s.svc.Logf("%s", p.Note)
	}
	p.Apply(s.svc.Logf)
	s.loadKeymap(keymap)
	s.svc.Logf("Changed virtual console keymap to '%s' toggle '%s'", keymap, toggle)
	names := []string{"VConsoleKeymap", "VConsoleKeymapToggle"}
	if p.XNeedsUpdate {
		names = append(names, x11Props...)
	}
	s.svc.Emit(names...)
	return nil
}

// SetX11Keyboard sets the X11 keyboard layout, which GNOME's login screen
// and mutter read. With convert, the console keymap follows through
// kbd-model-map when it has a row for the layout.
func (s *server) SetX11Keyboard(sender dbus.Sender, layout, model, variant, options string, convert, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
	in := locale.X11{Layout: layout, Model: model, Variant: variant, Options: options}
	if !locale.SafeX11(in) {
		return dbusservice.InvalidArgs("Invalid X11 keyboard layout.")
	}
	if p, err := locale.PlanX11(in, convert); err != nil {
		return dbusservice.Failed("Failed to read virtual console keymap data: %v", err)
	} else if !p.Change {
		return nil
	}
	if err := s.pk.Authorize(sender, iface+".set-keyboard", nil, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := locale.PlanX11(in, convert)
	if err != nil {
		return dbusservice.Failed("Failed to read virtual console keymap data: %v", err)
	}
	if !p.Change {
		return nil
	}
	if p.Note != "" {
		s.svc.Logf("%s", p.Note)
	}
	if err := p.Apply(s.svc.Logf); err != nil {
		return dbusservice.Failed("%v", err)
	}
	if p.VCChange {
		s.loadKeymap(p.VC.Keymap)
	}
	s.svc.Logf("Changed X11 keyboard layout to '%s' model '%s' variant '%s' options '%s'", layout, model, variant, options)
	names := append([]string{}, x11Props...)
	if p.VCChange {
		names = append(names, "VConsoleKeymap", "VConsoleKeymapToggle")
	}
	s.svc.Emit(names...)
	return nil
}
