// Package timedate holds what slinit-timedatectl and slinit-timedated share:
// the time zone, the RTC and its /etc/adjtime mode, the system clock, and
// the time-sync service. The paths are variables so tests can point them at
// a scratch tree.
package timedate

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// atomicSymlink creates a symlink at path pointing to target, using
// the tmp-name + rename dance so readers never see a partial state.
func atomicSymlink(target, path string) error {
	dir := "."
	if idx := strings.LastIndex(path, "/"); idx > 0 {
		dir = path[:idx]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".timedatectl.")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()
	os.Remove(tmpName) // Symlink target must not exist yet.
	if err := os.Symlink(target, tmpName); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// WriteRTCFromSystem writes the current wall-clock time to /dev/rtc,
// interpreting it as UTC or local per the `inLocal` flag. Uses the
// hwclock(8) trick: shell out to hwclock when it is available, since
// RTC_SET_TIME requires CAP_SYS_TIME and slightly nontrivial ioctl
// plumbing that x/sys/unix does not wrap. Falls back to a friendly
// "hwclock missing" error rather than pretending success.
func WriteRTCFromSystem(inLocal bool) error {
	if _, err := exec.LookPath("hwclock"); err != nil {
		return errors.New("hwclock(8) not available; install util-linux to use --adjust-system-clock")
	}
	arg := "--utc"
	if inLocal {
		arg = "--localtime"
	}
	cmd := exec.Command("hwclock", "--systohc", arg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("hwclock: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// FindNTPService walks a known list of time-sync service names and
// returns the first one that has a config file under /etc/slinit.d/.
// Probe order matches likelihood on a slinit host.
func FindNTPService() string {
	for _, name := range ntpServices {
		for _, dir := range SlinitServiceDirs {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return name
			}
		}
	}
	return ""
}

// ServiceRunning asks slinitctl whether the named service is up. Any
// non-zero exit is treated as "not running" — we don't want status
// output to fail just because the daemon isn't up yet.
func ServiceRunning(name string) bool {
	cmd := exec.Command(SlinitctlBin, "is-started", name)
	return cmd.Run() == nil
}

// slinitctl invokes `slinitctl SUB NAME` and surfaces its output on
// failure so operators see the real reason.
func slinitctl(subcmd, name string) error {
	cmd := exec.Command(SlinitctlBin, subcmd, name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Overridable in tests / when running from a container-mode slinit
// with a non-default socket path.
var (
	// SlinitServiceDirs are where the system slinit looks for service
	// descriptions, in its order (cmd/slinit's resolveServiceDirs; a test
	// there keeps the two equal). Only /etc/slinit.d used to be searched,
	// so a time-sync service a package ships in /lib/slinit.d — chronyd
	// on Sunlight — was not found: CanNTP read false, `timedatectl` said
	// "NTP service: n/a", and GNOME's automatic time could not be turned
	// on, with chronyd running all along.
	SlinitServiceDirs = []string{"/etc/slinit.d", "/run/slinit.d", "/usr/local/lib/slinit.d", "/lib/slinit.d"}
	SlinitctlBin      = "slinitctl"
)

// ntpServices are the time-sync services recognised, in preference order.
var ntpServices = []string{"chronyd", "systemd-timesyncd", "ntpd", "openntpd", "sntp"}

// RCConfPath is the Void-style /etc/rc.conf. slinit-services' boot script
// re-points /etc/localtime from its TIMEZONE= every boot, so a time zone
// set only through the symlink would be undone at the next reboot.
var RCConfPath = "/etc/rc.conf"

// SetTimezone points /etc/localtime at zone, updates /etc/timezone where
// that file exists (Debian-family convention; it is not created), and
// rewrites TIMEZONE= in /etc/rc.conf where an uncommented one is set.
func SetTimezone(zone string) error {
	if err := ValidateZone(zone); err != nil {
		return err
	}
	if err := atomicSymlink(filepath.Join(ZoneinfoDir, zone), LocaltimePath); err != nil {
		return fmt.Errorf("write %s: %w", LocaltimePath, err)
	}
	if _, err := os.Stat(TimezoneFile); err == nil {
		if err := writeAtomic(TimezoneFile, []byte(zone+"\n"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", TimezoneFile, err)
		}
	}
	if err := setRCConfVar("TIMEZONE", zone); err != nil {
		return fmt.Errorf("update %s: %w", RCConfPath, err)
	}
	return nil
}

// setRCConfVar rewrites an uncommented NAME= assignment in /etc/rc.conf to
// NAME="value". A file without one is left alone: rc.conf then does not
// steer this setting, and adding a line would make it start to.
func setRCConfVar(name, value string) error {
	b, err := os.ReadFile(RCConfPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	changed := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "export ") {
			t = strings.TrimSpace(strings.TrimPrefix(t, "export "))
		}
		if strings.HasPrefix(t, name+"=") {
			lines[i] = name + "=\"" + value + "\""
			changed = true
		}
	}
	if !changed {
		return nil
	}
	fi, err := os.Stat(RCConfPath)
	if err != nil {
		return err
	}
	return writeAtomic(RCConfPath, []byte(strings.Join(lines, "\n")), fi.Mode().Perm())
}

// SystemFromRTC sets the system clock from the RTC, read as local time or
// UTC (hwclock --hctosys).
func SystemFromRTC(inLocal bool) error {
	if _, err := exec.LookPath("hwclock"); err != nil {
		return errors.New("hwclock(8) not available")
	}
	arg := "--utc"
	if inLocal {
		arg = "--localtime"
	}
	if out, err := exec.Command("hwclock", "--hctosys", arg).CombinedOutput(); err != nil {
		return fmt.Errorf("hwclock: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetNTP enables and starts, or disables and stops, the time-sync service
// FindNTPService finds.
func SetNTP(enable bool) error {
	svc := FindNTPService()
	if svc == "" {
		return fmt.Errorf("no known time-sync service (%s) in %s",
			strings.Join(ntpServices, ", "), strings.Join(SlinitServiceDirs, ", "))
	}
	en, act := "enable", "start"
	if !enable {
		en, act = "disable", "stop"
	}
	if err := slinitctl(en, svc); err != nil {
		return fmt.Errorf("slinitctl %s %s: %w", en, svc, err)
	}
	if err := slinitctl(act, svc); err != nil {
		return fmt.Errorf("slinitctl %s %s: %w", act, svc, err)
	}
	return nil
}
