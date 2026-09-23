package main

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// The bug this pins: `slinitctl shutdown softreboot --fast` on real
// hardware printed
//
//	[    7.065247] reboot: System halted
//
// and the machine was gone. A soft-reboot re-executes slinit in place —
// there is no reboot(2) command for it — but the force path called
// reboot(2) anyway, and the type mapping fell through to HALT. The same
// held for --superfast, and for ShutdownRemain.
//
// The haste flags ask to skip work on the way to the kernel, so they can
// only choose a syscall path when the kernel is where the request ends.
func TestSoftRebootNeverTakesASyscallPath(t *testing.T) {
	for _, st := range []service.ShutdownType{service.ShutdownSoftReboot, service.ShutdownRemain} {
		for _, flags := range []uint8{
			control.ShutdownFlagKill,
			control.ShutdownFlagFast,
			control.ShutdownFlagSuper,
		} {
			got := routeShutdown(st, flags, true, false)
			if got != routeKill {
				t.Errorf("%v with flags %#x routed to %v; want routeKill — "+
					"a syscall route halts the machine instead", st, flags, got)
			}
		}
	}
}

func TestRouteShutdownPicksThePathTheOperatorAskedFor(t *testing.T) {
	cases := []struct {
		name      string
		st        service.ShutdownType
		flags     uint8
		isPID1    bool
		container bool
		want      shutdownRoute
	}{
		{"no flags is the graceful teardown", service.ShutdownHalt, 0, true, false, routeGraceful},
		{"now kills the services", service.ShutdownHalt, control.ShutdownFlagKill, true, false, routeKill},
		{"--fast skips the teardown", service.ShutdownHalt, control.ShutdownFlagFast, true, false, routeForce},
		{"--superfast skips the sync too", service.ShutdownPoweroff, control.ShutdownFlagSuper, true, false, routeImmediate},
		{"kexec is a kernel operation", service.ShutdownKexec, control.ShutdownFlagFast, true, false, routeForce},

		// Without a syscall to make, haste can only mean the teardown.
		{"container --fast", service.ShutdownHalt, control.ShutdownFlagFast, true, true, routeKill},
		{"container --superfast", service.ShutdownHalt, control.ShutdownFlagSuper, true, true, routeKill},
		{"not PID 1 --fast", service.ShutdownReboot, control.ShutdownFlagFast, false, false, routeKill},

		// And no flags still means graceful wherever it runs.
		{"container, no flags", service.ShutdownHalt, 0, true, true, routeGraceful},
		{"softreboot, no flags", service.ShutdownSoftReboot, 0, true, false, routeGraceful},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeShutdown(tc.st, tc.flags, tc.isPID1, tc.container); got != tc.want {
				t.Errorf("routeShutdown(%v, %#x, pid1=%v, container=%v) = %v, want %v",
					tc.st, tc.flags, tc.isPID1, tc.container, got, tc.want)
			}
		})
	}
}
