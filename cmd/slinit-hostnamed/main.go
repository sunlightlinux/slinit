// slinit-hostnamed serves org.freedesktop.hostname1, the interface GNOME
// Settings' About panel, NetworkManager, bluetoothd and GTK use to read the
// machine's names, chassis and hardware, and to change its hostname. It is
// systemd-hostnamed's interface, backed by the same code as
// slinit-hostnamectl (pkg/hostname), with each change authorized by polkit.
//
// It is meant to be started by D-Bus activation and exits after a while
// without requests, as hostnamed does.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/dbusservice"
	"github.com/sunlightlinux/slinit/pkg/hostname"
	"github.com/sunlightlinux/slinit/pkg/polkit"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-hostnamed version %s\n", version)
		return
	}
	idle := flag.Duration("exit-idle", 30*time.Second, "exit after this long without requests (0: never)")
	fallback := flag.String("fallback-hostname", hostname.FallbackHostname,
		"hostname used when /etc/hostname and os-release's DEFAULT_HOSTNAME name none")
	flag.Parse()
	hostname.FallbackHostname = *fallback
	dbusservice.EnsurePath()
	if err := run(*idle); err != nil {
		dbusservice.Fatal("slinit-hostnamed", err)
	}
}

func run(idle time.Duration) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect to the system bus: %w", err)
	}
	defer conn.Close()
	s := newServer(conn, polkit.New(conn))
	s.svc.OpenSyslog()
	if err := s.svc.Export(s); err != nil {
		return err
	}
	if err := s.svc.Claim(busName); err != nil {
		return err
	}
	s.svc.WaitIdle(idle)
	return nil
}
