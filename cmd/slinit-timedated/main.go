// slinit-timedated serves org.freedesktop.timedate1, the D-Bus interface
// GNOME Settings' Date & Time panel and gnome-settings-daemon's automatic
// time zone use to change the time zone, the clock, the RTC mode and NTP.
// It is systemd-timedated's interface, backed by the same code as
// slinit-timedatectl (pkg/timedate), with each change authorized by polkit.
//
// It is meant to be started by D-Bus activation and exits after a while
// without requests, as timedated does.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"

	"github.com/sunlightlinux/slinit/pkg/polkit"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-timedated version %s\n", version)
		return
	}
	idle := flag.Duration("exit-idle", 30*time.Second, "exit after this long without requests (0: never)")
	flag.Parse()
	if err := run(*idle); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-timedated: %v\n", err)
		os.Exit(1)
	}
}

func run(idle time.Duration) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect to the system bus: %w", err)
	}
	defer conn.Close()
	s := &server{conn: conn, pk: polkit.New(conn), m: realMachine()}
	if err := export(conn, s); err != nil {
		return err
	}
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("request %s: %w", busName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s is already owned", busName)
	}
	s.lastUse.Store(time.Now().UnixNano())
	waitIdle(s, idle)
	return nil
}

// export puts the methods, the properties and the introspection data on
// the object path.
func export(conn *dbus.Conn, s *server) error {
	if err := conn.Export(s, objPath, iface); err != nil {
		return err
	}
	if err := conn.Export(props{s}, objPath, propIface); err != nil {
		return err
	}
	node := &introspect.Node{
		Name: string(objPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{Name: propIface, Methods: introspect.Methods(props{s})},
			{Name: iface, Methods: introspect.Methods(s), Properties: introspectProps()},
		},
	}
	return conn.Export(introspect.NewIntrospectable(node), objPath, "org.freedesktop.DBus.Introspectable")
}

func introspectProps() []introspect.Property {
	sig := map[string]string{"Timezone": "s", "LocalRTC": "b", "CanNTP": "b", "NTP": "b",
		"NTPSynchronized": "b", "TimeUSec": "t", "RTCTimeUSec": "t"}
	emits := map[string]string{"Timezone": "true", "LocalRTC": "true", "NTP": "true"}
	var out []introspect.Property
	for _, n := range propertyNames {
		p := introspect.Property{Name: n, Type: sig[n], Access: "read"}
		e := emits[n]
		if e == "" {
			e = "false"
		}
		p.Annotations = []introspect.Annotation{{Name: "org.freedesktop.DBus.Property.EmitsChangedSignal", Value: e}}
		out = append(out, p)
	}
	return out
}

// waitIdle returns once no request has been in flight for idle. With idle
// zero it never returns.
func waitIdle(s *server, idle time.Duration) {
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
