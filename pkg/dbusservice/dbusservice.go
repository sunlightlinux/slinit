// Package dbusservice is the scaffolding slinit's settings daemons share
// (slinit-timedated, slinit-hostnamed, slinit-localed): one object with one
// interface, read-only properties computed when asked, PropertiesChanged
// for the ones that announce changes, introspection, and an exit once the
// service has been idle, as systemd's bus-activated daemons do.
package dbusservice

import (
	"errors"
	"fmt"
	"log/syslog"
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

	syslog infoWriter // set by OpenSyslog
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

// Logf writes a line to stderr and, once OpenSyslog has been called, to
// syslog. stderr alone is not enough: these daemons are started by D-Bus
// activation, so their parent is dbus-daemon, whose stderr is usually
// /dev/null, and every "Changed system time ..." line was lost. slinit
// serves /dev/log into the journal, so syslog is where they can be read.
func (s *Service) Logf(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	fmt.Fprintln(os.Stderr, s.Name+": "+line)
	if s.syslog != nil {
		_ = s.syslog.Info(line)
	}
}

// infoWriter is the part of *syslog.Writer Logf uses.
type infoWriter interface{ Info(string) error }

// OpenSyslog makes Logf also write to syslog, tagged with the service
// name. A daemon's main calls it; tests do not, so they do not write to
// the host's log. Without /dev/log it does nothing.
func (s *Service) OpenSyslog() {
	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, s.Name); err == nil {
		s.syslog = w
	}
}

// DefaultPath is systemd's default search path for the programs a service
// runs, used when the environment gives none.
const DefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// EnsurePath sets PATH to DefaultPath when it is unset or empty, and
// reports whether it did. dbus-daemon's activation helper starts a
// service with an emptied environment, so these daemons had no PATH and
// every program they run by name failed: timedated could not ask
// slinitctl whether chronyd ran (NTP read false, SetNTP failed, a manual
// SetTime was not refused while chronyd kept time) nor run hwclock, and
// localed could not run locale -a or loadkeys. A daemon's main calls it
// before anything else.
func EnsurePath() bool {
	if os.Getenv("PATH") != "" {
		return false
	}
	os.Setenv("PATH", DefaultPath)
	return true
}

// Fatal reports a daemon's start-up failure on stderr and in syslog, for
// the same reason as Logf, and exits 1.
func Fatal(name string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
	if w, serr := syslog.New(syslog.LOG_ERR|syslog.LOG_DAEMON, name); serr == nil {
		_ = w.Err(err.Error())
		w.Close()
	}
	os.Exit(1)
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

// WaitIdle returns nil once no request has been in flight for idle; with
// idle zero it returns only when the bus connection is lost. Losing it
// is an error, so the daemon exits non-zero and is started again rather
// than staying up with nothing on the bus.
func (s *Service) WaitIdle(idle time.Duration) error {
	lost := s.Conn.Context().Done()
	var tick <-chan time.Time
	if idle > 0 {
		t := time.NewTicker(idle / 4)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-lost:
			return errors.New("lost the bus connection")
		case <-tick:
		}
		last := time.Unix(0, s.lastUse.Load())
		if s.inflight.Load() == 0 && time.Since(last) >= idle {
			return nil
		}
	}
}
