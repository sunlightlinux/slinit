// set.go — setter handlers for time, timezone, local-RTC flag, and
// NTP enable/disable, plus list-timezones.
//
// set-time uses clock_settime(CLOCK_REALTIME); requires CAP_SYS_TIME.
// set-timezone swaps /etc/localtime atomically via a tmp-symlink +
// rename dance, and optionally writes /etc/timezone (Debian-family
// distros consult that file for hostname-independent timezone
// display). set-local-rtc rewrites /etc/adjtime line 3, and — with
// --adjust-system-clock — nudges the RTC to the new interpretation
// via hwclock's convention (write current wall time expressed under
// the new mode).
//
// set-ntp is a best-effort adapter: for each of a small list of
// known time-sync services (systemd-timesyncd, chronyd, ntpd,
// openntpd, sntp), check whether it exists under /etc/slinit.d/ and
// enable/disable + start/stop the first one found. Failure to find
// any is reported clearly rather than silently succeeding.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// runSetTime parses TIME per systemd's accepted forms and calls
// clock_settime(CLOCK_REALTIME).
func runSetTime(opts options) error {
	if len(opts.args) == 0 {
		return errors.New("set-time requires a TIME argument")
	}
	t, err := parseTime(opts.args[0], time.Now)
	if err != nil {
		return err
	}
	ts := unix.Timespec{Sec: int64(t.Unix()), Nsec: int64(t.Nanosecond())}
	if err := unix.ClockSettime(unix.CLOCK_REALTIME, &ts); err != nil {
		return fmt.Errorf("clock_settime: %w", err)
	}
	return nil
}

// parseTime accepts (in order of specificity):
//   - @EPOCH        seconds since 1970 (float ok, but truncated)
//   - RFC3339       2006-01-02T15:04:05Z07:00
//   - systemd form  2006-01-02 15:04:05  (local TZ)
//   - short form    15:04:05             (today, local TZ)
//   - relative      +5min, -2h, +30s
//   - special       "now" (yields time.Now)
//
// The `now` injection point makes the parser deterministic under
// unit test.
func parseTime(s string, now func() time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "now":
		return now(), nil
	case strings.HasPrefix(s, "@"):
		f, err := strconv.ParseFloat(s[1:], 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid @epoch %q: %w", s, err)
		}
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		return time.Unix(sec, nsec), nil
	case strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-"):
		d, err := parseRelative(s)
		if err != nil {
			return time.Time{}, err
		}
		return now().Add(d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("15:04:05", s, time.Local); err == nil {
		n := now()
		return time.Date(n.Year(), n.Month(), n.Day(),
			t.Hour(), t.Minute(), t.Second(), 0, time.Local), nil
	}
	if t, err := time.ParseInLocation("15:04", s, time.Local); err == nil {
		n := now()
		return time.Date(n.Year(), n.Month(), n.Day(),
			t.Hour(), t.Minute(), 0, 0, time.Local), nil
	}
	return time.Time{}, fmt.Errorf("cannot parse time %q", s)
}

// parseRelative accepts `+5min`, `-2h`, `+1d`, `+30s`, matching the
// suffix vocabulary systemd-analyze timespan documents. Go's
// time.ParseDuration handles ns/us/ms/s/m/h natively but does not
// grok `min`, `d`, `day`, `days`, `w`, `week(s)` — this splits the
// leading sign+number from the trailing unit and applies a lookup
// table so `+1d` doesn't get mangled by a naive substring replace.
func parseRelative(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Split into leading sign+digits[.fraction] and trailing unit.
	i := 0
	if s[0] == '+' || s[0] == '-' {
		i++
	}
	numStart := i
	for i < len(s) {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' {
			i++
			continue
		}
		break
	}
	if i == numStart {
		return 0, fmt.Errorf("no digits in duration %q", s)
	}
	numPart := s[:i]
	unit := strings.ToLower(s[i:])
	multipliers := map[string]time.Duration{
		"s": time.Second, "sec": time.Second, "second": time.Second, "seconds": time.Second,
		"m": time.Minute, "min": time.Minute, "minute": time.Minute, "minutes": time.Minute,
		"h": time.Hour, "hr": time.Hour, "hour": time.Hour, "hours": time.Hour,
		"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
		"w": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour, "weeks": 7 * 24 * time.Hour,
		"ms": time.Millisecond, "msec": time.Millisecond,
		"us": time.Microsecond, "usec": time.Microsecond,
		"": time.Second, // bare number → seconds, per systemd-analyze
	}
	mul, ok := multipliers[unit]
	if !ok {
		return 0, fmt.Errorf("unknown duration unit %q in %q", unit, s)
	}
	f, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return time.Duration(f * float64(mul)), nil
}

// runSetTimezone validates + atomically swaps /etc/localtime, and
// writes /etc/timezone when the file already exists (creating it
// would surprise on distros that pin to just the symlink).
func runSetTimezone(opts options) error {
	if len(opts.args) == 0 {
		return errors.New("set-timezone requires a ZONE argument")
	}
	zone := opts.args[0]
	if err := validateZone(zone); err != nil {
		return err
	}
	target := filepath.Join(zoneinfoDir, zone)
	if err := atomicSymlink(target, localtimeSym); err != nil {
		return fmt.Errorf("write %s: %w", localtimeSym, err)
	}
	// /etc/timezone is Debian-family convention; only update if
	// present, to avoid creating spurious files on distros that
	// don't use it.
	if _, err := os.Stat(timezoneFile); err == nil {
		if err := writeAtomic(timezoneFile, []byte(zone+"\n"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", timezoneFile, err)
		}
	}
	return nil
}

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

// runSetLocalRTC parses BOOL, writes /etc/adjtime, and — when
// --adjust-system-clock is set — re-writes the RTC hardware register
// so the previously-recorded fields carry the new interpretation.
func runSetLocalRTC(opts options) error {
	if len(opts.args) == 0 {
		return errors.New("set-local-rtc requires a BOOL argument")
	}
	b, err := parseBool(opts.args[0])
	if err != nil {
		return err
	}
	mode := "UTC"
	if b {
		mode = "LOCAL"
	}
	if err := writeAdjtimeMode(mode); err != nil {
		return err
	}
	if opts.adjustClock {
		if err := writeRTCFromWall(b); err != nil {
			return fmt.Errorf("adjust RTC: %w", err)
		}
	}
	return nil
}

// writeRTCFromWall writes the current wall-clock time to /dev/rtc,
// interpreting it as UTC or local per the `inLocal` flag. Uses the
// hwclock(8) trick: shell out to hwclock when it is available, since
// RTC_SET_TIME requires CAP_SYS_TIME and slightly nontrivial ioctl
// plumbing that x/sys/unix does not wrap. Falls back to a friendly
// "hwclock missing" error rather than pretending success.
func writeRTCFromWall(inLocal bool) error {
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

// runSetNTP enables or disables the local time-sync service. Which
// service that is depends on what's installed; we probe a fixed
// small list (systemd-timesyncd, chronyd, ntpd, openntpd, sntp) and
// operate on the first match.
func runSetNTP(opts options) error {
	if len(opts.args) == 0 {
		return errors.New("set-ntp requires a BOOL argument")
	}
	b, err := parseBool(opts.args[0])
	if err != nil {
		return err
	}
	svc := findNTPService()
	if svc == "" {
		return errors.New("no known time-sync service found under /etc/slinit.d/ (systemd-timesyncd, chronyd, ntpd, openntpd, sntp)")
	}
	action := "start"
	enable := "enable"
	if !b {
		action = "stop"
		enable = "disable"
	}
	if err := slinitctl(enable, svc); err != nil {
		return fmt.Errorf("slinitctl %s %s: %w", enable, svc, err)
	}
	if err := slinitctl(action, svc); err != nil {
		return fmt.Errorf("slinitctl %s %s: %w", action, svc, err)
	}
	return nil
}

// runListTimezones prints the zone list one per line.
func runListTimezones(out io.Writer, opts options) error {
	zones, err := listTimezones()
	if err != nil {
		return err
	}
	for _, z := range zones {
		fmt.Fprintln(out, z)
	}
	return nil
}

// parseBool accepts the same tokens systemd does — 0/1, yes/no,
// true/false, on/off, case-insensitive.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "1", "yes", "true", "on":
		return true, nil
	case "0", "no", "false", "off":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q (want yes/no, true/false, 1/0, on/off)", s)
}

// detectNTPService returns (name, running) for the first NTP-shaped
// service present on the host. Used only by status output — set-ntp
// uses findNTPService for the same probe minus the running check.
func detectNTPService() (string, bool) {
	svc := findNTPService()
	if svc == "" {
		return "", false
	}
	return svc, isRunning(svc)
}

// findNTPService walks a known list of time-sync service names and
// returns the first one that has a config file under /etc/slinit.d/.
// Probe order matches likelihood on a slinit host.
func findNTPService() string {
	for _, name := range []string{
		"chronyd", "systemd-timesyncd", "ntpd", "openntpd", "sntp",
	} {
		if _, err := os.Stat(filepath.Join(slinitConfDir, name)); err == nil {
			return name
		}
	}
	return ""
}

// isRunning asks slinitctl whether the named service is up. Any
// non-zero exit is treated as "not running" — we don't want status
// output to fail just because the daemon isn't up yet.
func isRunning(name string) bool {
	cmd := exec.Command(slinitctlBin, "is-started", name)
	return cmd.Run() == nil
}

// slinitctl invokes `slinitctl SUB NAME` and surfaces its output on
// failure so operators see the real reason.
func slinitctl(subcmd, name string) error {
	cmd := exec.Command(slinitctlBin, subcmd, name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Overridable in tests / when running from a container-mode slinit
// with a non-default socket path.
var (
	slinitConfDir = "/etc/slinit.d"
	slinitctlBin  = "slinitctl"
)
