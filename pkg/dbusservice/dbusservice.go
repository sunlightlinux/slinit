// Package dbusservice is the scaffolding slinit's settings daemons share
// (slinit-timedated, slinit-hostnamed, slinit-localed): one object with one
// interface, read-only properties computed when asked, PropertiesChanged
// for the ones that announce changes, introspection, and an exit once the
// service has been idle, as systemd's bus-activated daemons do.
package dbusservice

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// PropIface is org.freedesktop.DBus.Properties.
const PropIface = "org.freedesktop.DBus.Properties"

// Property describes one read-only property.
type Property struct {
	Name  string
	Sig   string // D-Bus signature, e.g. "s"
	Emits bool   // announced with PropertiesChanged
}

// Service is one exported object.
type Service struct {
	Name  string // the program, for log lines
	Conn  *dbus.Conn
	Path  dbus.ObjectPath
	Iface string
	Props []Property
	// Get returns a property's current value.
	Get func(name string) (dbus.Variant, bool)
	// Now is the clock; tests may replace it.
	Now func() time.Time

	inflight atomic.Int32
	lastUse  atomic.Int64
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Touch marks a request in flight; call the returned function when it
// ends. A request in flight, such as one waiting on a polkit password
// prompt, keeps the service from exiting.
func (s *Service) Touch() func() {
	s.inflight.Add(1)
	s.lastUse.Store(s.now().UnixNano())
	return func() {
		s.lastUse.Store(s.now().UnixNano())
		s.inflight.Add(-1)
	}
}

// Logf writes a line to stderr, which slinit puts in the service's log.
func (s *Service) Logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, s.Name+": "+format+"\n", a...)
}

// Emit sends PropertiesChanged for names, with their current values, as
// sd_bus_emit_properties_changed does.
func (s *Service) Emit(names ...string) {
	if s.Conn == nil {
		return
	}
	changed := map[string]dbus.Variant{}
	for _, n := range names {
		if v, ok := s.Get(n); ok {
			changed[n] = v
		}
	}
	_ = s.Conn.Emit(s.Path, PropIface+".PropertiesChanged", s.Iface, changed, []string{})
}

// InvalidArgs is org.freedesktop.DBus.Error.InvalidArgs.
func InvalidArgs(format string, a ...any) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{fmt.Sprintf(format, a...)})
}

// Failed is org.freedesktop.DBus.Error.Failed.
func Failed(format string, a ...any) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.Failed", []any{fmt.Sprintf(format, a...)})
}

// props serves org.freedesktop.DBus.Properties; every property is
// read-only and changed only through the interface's methods.
type props struct{ s *Service }

func (p props) Get(ifc, name string) (dbus.Variant, *dbus.Error) {
	defer p.s.Touch()()
	if ifc != p.s.Iface {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", []any{ifc})
	}
	v, ok := p.s.Get(name)
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", []any{name})
	}
	return v, nil
}

func (p props) GetAll(ifc string) (map[string]dbus.Variant, *dbus.Error) {
	defer p.s.Touch()()
	out := map[string]dbus.Variant{}
	if ifc != p.s.Iface && ifc != "" {
		return out, nil
	}
	for _, pr := range p.s.Props {
		if v, ok := p.s.Get(pr.Name); ok {
			out[pr.Name] = v
		}
	}
	return out, nil
}

func (p props) Set(_, name string, _ dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", []any{name})
}

// Properties returns the org.freedesktop.DBus.Properties implementation,
// for tests that call it directly.
func (s *Service) Properties() interface {
	Get(string, string) (dbus.Variant, *dbus.Error)
	GetAll(string) (map[string]dbus.Variant, *dbus.Error)
	Set(string, string, dbus.Variant) *dbus.Error
} {
	return props{s}
}

// Export puts methods (an object whose exported methods form the
// interface), the properties and the introspection data on the path.
func (s *Service) Export(methods any) error {
	if err := s.Conn.Export(methods, s.Path, s.Iface); err != nil {
		return err
	}
	if err := s.Conn.Export(props{s}, s.Path, PropIface); err != nil {
		return err
	}
	var ip []introspect.Property
	for _, pr := range s.Props {
		e := "false"
		if pr.Emits {
			e = "true"
		}
		ip = append(ip, introspect.Property{Name: pr.Name, Type: pr.Sig, Access: "read",
			Annotations: []introspect.Annotation{{Name: "org.freedesktop.DBus.Property.EmitsChangedSignal", Value: e}}})
	}
	node := &introspect.Node{
		Name: string(s.Path),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{Name: PropIface, Methods: introspect.Methods(props{s})},
			{Name: s.Iface, Methods: introspect.Methods(methods), Properties: ip},
		},
	}
	return s.Conn.Export(introspect.NewIntrospectable(node), s.Path, "org.freedesktop.DBus.Introspectable")
}

// Claim takes the bus name, failing if someone else owns it.
func (s *Service) Claim(busName string) error {
	reply, err := s.Conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("request %s: %w", busName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s is already owned", busName)
	}
	s.lastUse.Store(s.now().UnixNano())
	return nil
}

// WaitIdle returns once no request has been in flight for idle; with idle
// zero it never returns.
func (s *Service) WaitIdle(idle time.Duration) {
	if idle <= 0 {
		select {}
	}
	for {
		time.Sleep(idle / 4)
		last := time.Unix(0, s.lastUse.Load())
		if s.inflight.Load() == 0 && time.Since(last) >= idle {
			return
		}
	}
}
