// slinit-localed serves org.freedesktop.locale1, the interface GNOME
// Settings' Region panel, GNOME Shell's login screen, mutter and
// gnome-settings-daemon use to read and set the system locale and the
// keyboard layout. It is systemd-localed's interface, backed by pkg/locale
// (shared with slinit-localectl), with each change authorized by polkit.
//
// It is meant to be started by D-Bus activation and exits after a while
// without requests, as localed does.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/dbusservice"
	"github.com/sunlightlinux/slinit/pkg/polkit"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-localed version %s\n", version)
		return
	}
	idle := flag.Duration("exit-idle", 30*time.Second, "exit after this long without requests (0: never)")
	flag.Parse()
	dbusservice.EnsurePath()
	if err := run(*idle); err != nil {
		dbusservice.Fatal("slinit-localed", err)
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
