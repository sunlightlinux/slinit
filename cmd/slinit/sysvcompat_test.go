package main

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestParseSysVCompat_Halt(t *testing.T) {
	st, prog, ok := parseSysVCompat([]string{"/sbin/halt"})
	if !ok {
		t.Fatal("halt should be recognised")
	}
	if prog != "halt" {
		t.Errorf("prog = %q, want halt", prog)
	}
	if st != service.ShutdownHalt {
		t.Errorf("st = %v, want ShutdownHalt", st)
	}
}

func TestParseSysVCompat_Poweroff(t *testing.T) {
	st, _, ok := parseSysVCompat([]string{"poweroff"})
	if !ok || st != service.ShutdownPoweroff {
		t.Errorf("poweroff → ok=%v st=%v, want true/ShutdownPoweroff", ok, st)
	}
}

func TestParseSysVCompat_Reboot(t *testing.T) {
	st, _, ok := parseSysVCompat([]string{"/usr/sbin/reboot"})
	if !ok || st != service.ShutdownReboot {
		t.Errorf("reboot → ok=%v st=%v, want true/ShutdownReboot", ok, st)
	}
}

func TestParseSysVCompat_HaltMinusP(t *testing.T) {
	// `halt -p` is the busybox/sysvinit way to request poweroff.
	st, _, ok := parseSysVCompat([]string{"halt", "-p"})
	if !ok || st != service.ShutdownPoweroff {
		t.Errorf("halt -p → ok=%v st=%v, want true/ShutdownPoweroff", ok, st)
	}

	// Uppercase -P should behave the same way.
	st, _, ok = parseSysVCompat([]string{"halt", "-P"})
	if !ok || st != service.ShutdownPoweroff {
		t.Errorf("halt -P → st=%v, want ShutdownPoweroff", st)
	}
}

func TestParseSysVCompat_PoweroffMinusR(t *testing.T) {
	st, _, ok := parseSysVCompat([]string{"poweroff", "-r"})
	if !ok || st != service.ShutdownReboot {
		t.Errorf("poweroff -r → st=%v, want ShutdownReboot", st)
	}
}

func TestParseSysVCompat_RebootMinusH_IgnoredForReboot(t *testing.T) {
	// `reboot -h` is nonsensical — reboot should stay reboot, not be
	// downgraded to halt.
	st, _, ok := parseSysVCompat([]string{"reboot", "-h"})
	if !ok || st != service.ShutdownReboot {
		t.Errorf("reboot -h → st=%v, want ShutdownReboot (unchanged)", st)
	}
}

func TestParseSysVCompat_HaltMinusH_StaysHalt(t *testing.T) {
	st, _, ok := parseSysVCompat([]string{"halt", "-h"})
	if !ok || st != service.ShutdownHalt {
		t.Errorf("halt -h → st=%v, want ShutdownHalt", st)
	}
}

func TestParseSysVCompat_UnknownFlagsTolerated(t *testing.T) {
	// sysvinit's -f (force), -n (no sync), -w (no-wtmp) must not cause
	// us to reject the invocation — they're accepted and ignored.
	st, _, ok := parseSysVCompat([]string{"poweroff", "-f", "-n", "-w", "--no-wall"})
	if !ok || st != service.ShutdownPoweroff {
		t.Errorf("poweroff -f -n -w --no-wall → ok=%v st=%v, want true/ShutdownPoweroff", ok, st)
	}
}

func TestParseSysVCompat_NotCompat(t *testing.T) {
	for _, name := range []string{"slinit", "slinitctl", "bash", "init"} {
		_, _, ok := parseSysVCompat([]string{"/sbin/" + name})
		if ok {
			t.Errorf("%q should NOT be dispatched as SysV compat", name)
		}
	}
}

func TestParseSysVCompat_EmptyArgv(t *testing.T) {
	_, _, ok := parseSysVCompat(nil)
	if ok {
		t.Error("empty argv should not be compat")
	}
	_, _, ok = parseSysVCompat([]string{})
	if ok {
		t.Error("empty argv slice should not be compat")
	}
}

func TestParseSysVCompat_LastFlagWins(t *testing.T) {
	// `halt -p -r`: the later flag wins, same as busybox's argv scan.
	st, _, ok := parseSysVCompat([]string{"halt", "-p", "-r"})
	if !ok || st != service.ShutdownReboot {
		t.Errorf("halt -p -r → st=%v, want ShutdownReboot (last flag wins)", st)
	}
}

func TestParseSysVCompat_BasenameStripsPath(t *testing.T) {
	// Verify that a full path with a basename match still dispatches,
	// so `./halt` and `/usr/local/sbin/halt` both work.
	for _, p := range []string{"halt", "./halt", "/sbin/halt", "/usr/local/sbin/halt"} {
		_, prog, ok := parseSysVCompat([]string{p})
		if !ok || prog != "halt" {
			t.Errorf("%q → ok=%v prog=%q, want true/halt", p, ok, prog)
		}
	}
}
