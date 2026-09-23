package main

import (
	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
)

// shutdownRoute names what a shutdown request should actually do.
type shutdownRoute int

const (
	// routeGraceful stops every service the usual way.
	routeGraceful shutdownRoute = iota
	// routeKill starts the teardown and SIGKILLs the services at once.
	routeKill
	// routeForce skips the teardown: sync and the syscall (`reboot -f`).
	routeForce
	// routeImmediate is the syscall alone, not even a sync (`reboot -ff`).
	routeImmediate
)

// routeShutdown decides which of the four a request lands on.
//
// The haste flags ask to skip work on the way to the kernel, so they
// only mean something when the kernel is where this ends:
//
//   - a soft-reboot re-executes slinit in place and ShutdownRemain keeps
//     the machine up; neither is a reboot(2) operation. Sending one down
//     the force path halted the machine instead — `slinitctl shutdown
//     softreboot --fast` printed "reboot: System halted". They take the
//     kill route, where the haste applies to the teardown, which is the
//     only part a soft-reboot has;
//   - in a container, and anywhere slinit is not PID 1, there is no
//     syscall to make at all, so the same applies to every type.
func routeShutdown(st service.ShutdownType, flags uint8, isPID1, containerMode bool) shutdownRoute {
	hurried := flags&(control.ShutdownFlagSuper|control.ShutdownFlagFast|control.ShutdownFlagKill) != 0
	if !hurried {
		return routeGraceful
	}

	_, kernelOp := shutdown.KernelShutdownCmd(st)
	canSyscall := isPID1 && !containerMode && kernelOp
	if !canSyscall {
		return routeKill
	}

	switch {
	case flags&control.ShutdownFlagSuper != 0:
		return routeImmediate
	case flags&control.ShutdownFlagFast != 0:
		return routeForce
	default:
		return routeKill
	}
}
