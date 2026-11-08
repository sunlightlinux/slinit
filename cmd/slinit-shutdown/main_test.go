package main

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// shutdown(8)'s time means now, or it is refused: never a later time taken
// for now.
func TestCheckShutdownTime(t *testing.T) {
	for _, ok := range []string{"now", "+0", "0"} {
		if err := checkShutdownTime(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"+5", "23:00", "soon", "+10"} {
		if err := checkShutdownTime(bad); err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestShutdownDashH(t *testing.T) {
	for _, c := range []struct{ from, want service.ShutdownType }{
		{service.ShutdownPoweroff, service.ShutdownPoweroff},
		{service.ShutdownReboot, service.ShutdownPoweroff},
		{service.ShutdownHalt, service.ShutdownHalt},
	} {
		if got := shutdownDashH(c.from); got != c.want {
			t.Errorf("from %v: %v", c.from, got)
		}
	}
}
