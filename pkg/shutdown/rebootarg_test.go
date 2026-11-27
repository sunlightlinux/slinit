package shutdown

import (
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A reboot with reboot-argument set passes it to the kernel through
// RESTART2; a kernel that refuses RESTART2 still reboots; other types
// ignore the argument. It used to be logged and dropped.
func TestRebootArgumentGoesToTheKernel(t *testing.T) {
	origReboot, origRestart2 := rebootFunc, restart2Func
	defer func() {
		rebootFunc, restart2Func = origReboot, origRestart2
		SetRebootArgument("")
	}()
	var plain []int
	var with []string
	var restart2Err error
	rebootFunc = func(cmd int) error { plain = append(plain, cmd); return nil }
	restart2Func = func(arg string) error { with = append(with, arg); return restart2Err }

	SetRebootArgument("recovery")
	if err := rebootSystem(service.ShutdownReboot); err != nil {
		t.Fatal(err)
	}
	if len(with) != 1 || with[0] != "recovery" || len(plain) != 0 {
		t.Fatalf("reboot with an argument: RESTART2 %q, plain %v; want RESTART2 \"recovery\" only", with, plain)
	}

	restart2Err = syscall.EINVAL
	with, plain = nil, nil
	_ = rebootSystem(service.ShutdownReboot)
	if len(with) != 1 || len(plain) != 1 || plain[0] != syscall.LINUX_REBOOT_CMD_RESTART {
		t.Fatalf("RESTART2 refused: RESTART2 %q, plain %v; want a plain RESTART after it", with, plain)
	}

	with, plain = nil, nil
	_ = rebootSystem(service.ShutdownPoweroff)
	if len(with) != 0 || len(plain) != 1 || plain[0] != syscall.LINUX_REBOOT_CMD_POWER_OFF {
		t.Fatalf("poweroff: RESTART2 %q, plain %v; the argument is for a reboot only", with, plain)
	}

	SetRebootArgument("")
	with, plain = nil, nil
	_ = rebootSystem(service.ShutdownReboot)
	if len(with) != 0 || len(plain) != 1 {
		t.Fatalf("no argument: RESTART2 %q, plain %v; want a plain reboot", with, plain)
	}
}
