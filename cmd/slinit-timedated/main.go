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

	"github.com/sunlightlinux/slinit/pkg/dbusservice"
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
		dbusservice.Fatal("slinit-timedated", err)
	}
}

func run(idle time.Duration) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect to the system bus: %w", err)
	}
	defer conn.Close()
	s := newServer(conn, polkit.New(conn), realMachine())
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
