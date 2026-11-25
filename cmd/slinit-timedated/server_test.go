package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/polkit"
	"github.com/sunlightlinux/slinit/pkg/timedate"
)

// world is a scratch system: a zoneinfo tree with two real zones, an
// /etc with localtime, adjtime and rc.conf, a recorded machine, and a
// polkit that answers as told and remembers what it was asked.
type world struct {
	t         *testing.T
	etc       string
	s         *server
	allow     bool
	asked     []string
	details   []map[string]string
	setTime   []time.Time
	rtcWrites []bool // the inLocal argument of each RTC write
	sysFromRT []bool
	ntpSet    []bool
	ntpSvc    string
	ntpOn     bool
	onAsk     func() // runs while polkit is "prompting"
}

func copyZone(t *testing.T, dir, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("/usr/share/zoneinfo", name))
	if err != nil {
		t.Skipf("host zoneinfo missing %s: %v", name, err)
	}
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	zi := filepath.Join(root, "zoneinfo")
	etc := filepath.Join(root, "etc")
	os.MkdirAll(etc, 0o755)
	copyZone(t, zi, "Europe/Bucharest")
	copyZone(t, zi, "Europe/Paris")
	if err := os.Symlink(filepath.Join(zi, "Europe/Bucharest"), filepath.Join(etc, "localtime")); err != nil {
		t.Fatal(err)
	}
	for p, v := range map[*string]string{
		&timedate.ZoneinfoDir:   zi,
		&timedate.LocaltimePath: filepath.Join(etc, "localtime"),
		&timedate.TimezoneFile:  filepath.Join(etc, "timezone"),
		&timedate.AdjtimePath:   filepath.Join(etc, "adjtime"),
		&timedate.RCConfPath:    filepath.Join(etc, "rc.conf"),
	} {
		old := *p
		*p = v
		t.Cleanup(func() { *p = old })
	}
	oldLocal := time.Local
	t.Cleanup(func() { time.Local = oldLocal })

	w := &world{t: t, etc: etc, allow: true}
	now := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	w.s = newServer(nil, &polkit.Checker{
		UID: func(dbus.Sender) (uint32, error) { return 1000, nil },
		Check: func(_ dbus.Sender, action string, d map[string]string, _ bool) (bool, bool, error) {
			w.asked = append(w.asked, action)
			w.details = append(w.details, d)
			if w.onAsk != nil {
				w.onAsk()
			}
			return w.allow, !w.allow, nil
		},
	}, machine{
		setSystemTime: func(tm time.Time) error { w.setTime = append(w.setTime, tm); return nil },
		rtcFromSystem: func(l bool) error { w.rtcWrites = append(w.rtcWrites, l); return nil },
		systemFromRTC: func(l bool) error { w.sysFromRT = append(w.sysFromRT, l); return nil },
		readRTC:       func() (time.Time, bool) { return now, true },
		findNTP:       func() string { return w.ntpSvc },
		ntpRunning:    func(string) bool { return w.ntpOn },
		setNTP:        func(b bool) error { w.ntpSet = append(w.ntpSet, b); w.ntpOn = b; return nil },
		ntpSynced:     func() bool { return true },
		kernelTZ:      func(*time.Location) {},
		now:           func() time.Time { return now },
	},
	)
	return w
}

func errName(e *dbus.Error) string {
	if e == nil {
		return ""
	}
	return e.Name
}

func TestSetTimezone(t *testing.T) {
	w := newWorld(t)
	os.WriteFile(filepath.Join(w.etc, "rc.conf"), []byte("# Void rc.conf\nKEYMAP=\"us\"\nTIMEZONE=\"Europe/Bucharest\"\n"), 0o644)

	if got := errName(w.s.SetTimezone(":1.9", "Mars/Olympus", true)); got != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Errorf("unknown zone: %q", got)
	}
	if got := errName(w.s.SetTimezone(":1.9", "../../etc/passwd", true)); got != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Errorf("path escape: %q", got)
	}
	if err := w.s.SetTimezone(":1.9", "Europe/Bucharest", true); err != nil || len(w.asked) != 0 {
		t.Errorf("the current zone must succeed without asking polkit: err=%v asked=%v", err, w.asked)
	}

	w.allow = false
	if got := errName(w.s.SetTimezone(":1.9", "Europe/Paris", false)); got != "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired" {
		t.Errorf("refused: %q", got)
	}
	if timedate.CurrentZone() != "Europe/Bucharest" {
		t.Error("a refused request changed the zone")
	}

	w.allow = true
	if err := w.s.SetTimezone(":1.9", "Europe/Paris", true); err != nil {
		t.Fatal(err)
	}
	if timedate.CurrentZone() != "Europe/Paris" {
		t.Errorf("zone is %q", timedate.CurrentZone())
	}
	if last := w.asked[len(w.asked)-1]; last != "org.freedesktop.timedate1.set-timezone" || w.details[len(w.details)-1]["zone"] != "Europe/Paris" {
		t.Errorf("polkit asked %s %v", last, w.details[len(w.details)-1])
	}
	rc, _ := os.ReadFile(filepath.Join(w.etc, "rc.conf"))
	if !strings.Contains(string(rc), `TIMEZONE="Europe/Paris"`) || !strings.Contains(string(rc), `KEYMAP="us"`) {
		t.Errorf("rc.conf not updated in place (the boot script would undo the change):\n%s", rc)
	}
	if len(w.rtcWrites) != 0 {
		t.Error("a UTC RTC does not need rewriting when the zone changes")
	}

	// With a local-time RTC the hardware clock has to follow.
	os.WriteFile(filepath.Join(w.etc, "adjtime"), []byte("0.0 0 0\n0\nLOCAL\n"), 0o644)
	if err := w.s.SetTimezone(":1.9", "Europe/Bucharest", true); err != nil {
		t.Fatal(err)
	}
	if len(w.rtcWrites) != 1 || !w.rtcWrites[0] {
		t.Errorf("a local RTC must be rewritten in local time, got %v", w.rtcWrites)
	}
}

func TestSetTimezoneLeavesRCConfWithoutTheKeyAlone(t *testing.T) {
	w := newWorld(t)
	orig := "# nothing set\n#TIMEZONE=\"Europe/Rome\"\n"
	os.WriteFile(filepath.Join(w.etc, "rc.conf"), []byte(orig), 0o644)
	if err := w.s.SetTimezone(":1.9", "Europe/Paris", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(w.etc, "rc.conf")); string(b) != orig {
		t.Errorf("rc.conf without an active TIMEZONE= was changed:\n%s", b)
	}
}

func TestSetTime(t *testing.T) {
	w := newWorld(t)
	w.ntpSvc, w.ntpOn = "chronyd", true
	if got := errName(w.s.SetTime(":1.9", 1, true, true)); got != "org.freedesktop.timedate1.AutomaticTimeSyncEnabled" {
		t.Errorf("with NTP running: %q", got)
	}
	w.ntpOn = false
	w.asked = nil
	for _, c := range []struct {
		usec     int64
		relative bool
		want     string
	}{
		{0, false, "org.freedesktop.DBus.Error.InvalidArgs"},
		{-5, false, "org.freedesktop.DBus.Error.InvalidArgs"},
		{time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro(), false, "org.freedesktop.DBus.Error.InvalidArgs"},
		{9223372036854775807, true, "org.freedesktop.DBus.Error.InvalidArgs"},
	} {
		if got := errName(w.s.SetTime(":1.9", c.usec, c.relative, true)); got != c.want {
			t.Errorf("SetTime(%d, rel=%v) = %q, want %q", c.usec, c.relative, got, c.want)
		}
	}
	if err := w.s.SetTime(":1.9", 0, true, true); err != nil || len(w.asked) != 0 || len(w.setTime) != 0 {
		t.Errorf("a zero relative change must do nothing and ask nothing: %v %v", err, w.asked)
	}
	if err := w.s.SetTime(":1.9", 90*1e6, true, true); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, 3, 1, 12, 1, 30, 0, time.UTC)
	if len(w.setTime) != 1 || !w.setTime[0].Equal(want) {
		t.Errorf("clock set to %v, want %v", w.setTime, want)
	}
	if len(w.rtcWrites) != 1 || w.rtcWrites[0] {
		t.Errorf("the RTC must follow the new time, in UTC here: %v", w.rtcWrites)
	}
	if w.asked[0] != "org.freedesktop.timedate1.set-time" {
		t.Errorf("asked %v", w.asked)
	}
}

// A relative SetTime was computed from the clock before polkit was asked,
// and a prompt can take as long as the user does: the clock was then set
// behind by the time spent answering. systemd re-runs the method once
// authorized, so the offset applies to the time it is set at, and NTP
// switched on meanwhile refuses it.
func TestSetTimeRelativeCountsFromAfterAuthorization(t *testing.T) {
	w := newWorld(t)
	clock := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	w.s.m.now = func() time.Time { return clock }
	w.onAsk = func() { clock = clock.Add(40 * time.Second) }
	if err := w.s.SetTime(":1.9", 90*1e6, true, true); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, 3, 1, 12, 2, 10, 0, time.UTC)
	if len(w.setTime) != 1 || !w.setTime[0].Equal(want) {
		t.Errorf("clock set to %v, want %v (90s after the prompt was answered)", w.setTime, want)
	}

	w.ntpSvc = "chronyd"
	w.onAsk = func() { w.ntpOn = true }
	if got := errName(w.s.SetTime(":1.9", 90*1e6, true, true)); got != "org.freedesktop.timedate1.AutomaticTimeSyncEnabled" {
		t.Errorf("NTP started during the prompt: %q, want AutomaticTimeSyncEnabled", got)
	}
}

// Whether NTP runs is asked of slinitctl, a process the daemon starts;
// it was asked before polkit, so a caller who may not set the time could
// make the daemon run it at will. It is now asked once polkit says yes.
func TestSetTimeAsksPolkitBeforeSlinitctl(t *testing.T) {
	w := newWorld(t)
	w.ntpSvc, w.allow = "chronyd", false
	probes := 0
	w.s.m.ntpRunning = func(string) bool { probes++; return w.ntpOn }
	if got := errName(w.s.SetTime(":1.9", 90*1e6, true, true)); got != "org.freedesktop.DBus.Error.AccessDenied" {
		t.Errorf("refused by polkit: %q, want AccessDenied", got)
	}
	if probes != 0 {
		t.Errorf("slinitctl asked %d times for a caller polkit refused", probes)
	}
	w.allow = true
	if err := w.s.SetTime(":1.9", 90*1e6, true, true); err != nil || probes != 1 {
		t.Errorf("allowed: err %v, slinitctl asked %d times, want once", err, probes)
	}
}

// time.Local was assigned on every time zone change while every other
// goroutine reads it (time.Now does): a data race, found by -race.
func TestSetTimezoneLeavesTimeLocal(t *testing.T) {
	w := newWorld(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = time.Now().String()
			}
		}
	}()
	before := time.Local
	for _, zone := range []string{"Europe/Paris", "Europe/Bucharest", "Europe/Paris"} {
		if err := w.s.SetTimezone(":1.9", zone, true); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
	if time.Local != before {
		t.Error("SetTimezone changed time.Local")
	}
}

func TestSetLocalRTC(t *testing.T) {
	w := newWorld(t)
	if err := w.s.SetLocalRTC(":1.9", false, false, true); err != nil || len(w.asked) != 0 {
		t.Errorf("no change must not ask polkit: %v %v", err, w.asked)
	}
	if err := w.s.SetLocalRTC(":1.9", true, false, true); err != nil {
		t.Fatal(err)
	}
	if !timedate.RTCInLocal() {
		t.Error("adjtime not switched to LOCAL")
	}
	if len(w.rtcWrites) != 1 || !w.rtcWrites[0] || len(w.sysFromRT) != 0 {
		t.Errorf("without fix_system the RTC is rewritten from the system clock: rtc=%v sys=%v", w.rtcWrites, w.sysFromRT)
	}
	if err := w.s.SetLocalRTC(":1.9", false, true, true); err != nil {
		t.Fatal(err)
	}
	if len(w.sysFromRT) != 1 || w.sysFromRT[0] {
		t.Errorf("with fix_system the system clock is set from the RTC: %v", w.sysFromRT)
	}
	if d := w.details[len(w.details)-1]; d["local_rtc"] != "0" {
		t.Errorf("details %v", d)
	}
}

func TestSetNTP(t *testing.T) {
	w := newWorld(t)
	if got := errName(w.s.SetNTP(":1.9", true, true)); got != "org.freedesktop.timedate1.NoNTPSupport" {
		t.Errorf("without a time-sync service: %q", got)
	}
	w.ntpSvc = "chronyd"
	w.allow = false
	if errName(w.s.SetNTP(":1.9", true, true)) == "" || len(w.ntpSet) != 0 {
		t.Error("a refused SetNTP touched the service")
	}
	w.allow = true
	if err := w.s.SetNTP(":1.9", true, true); err != nil || len(w.ntpSet) != 1 || !w.ntpSet[0] {
		t.Errorf("SetNTP(true): %v %v", err, w.ntpSet)
	}
}

func TestProperties(t *testing.T) {
	w := newWorld(t)
	w.ntpSvc, w.ntpOn = "chronyd", true
	all, err := w.s.svc.Properties().GetAll(iface)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"Timezone": "Europe/Bucharest", "LocalRTC": false, "CanNTP": true, "NTP": true,
		"NTPSynchronized": true,
		"TimeUSec":        uint64(time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC).UnixMicro()),
		"RTCTimeUSec":     uint64(time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC).UnixMicro()),
	}
	if len(all) != len(want) {
		t.Errorf("GetAll has %d properties, want %d", len(all), len(want))
	}
	for k, v := range want {
		if all[k].Value() != v {
			t.Errorf("%s = %#v, want %#v", k, all[k].Value(), v)
		}
	}
	if e := w.s.svc.Properties().Set(iface, "Timezone", dbus.MakeVariant("x")); errName(e) != "org.freedesktop.DBus.Error.PropertyReadOnly" {
		t.Errorf("Set: %v", e)
	}
}

func TestListTimezonesNeedsNoAuthorization(t *testing.T) {
	w := newWorld(t)
	zones, err := w.s.ListTimezones()
	if err != nil || len(w.asked) != 0 {
		t.Fatalf("%v %v", err, w.asked)
	}
	if strings.Join(zones, ",") != "Europe/Bucharest,Europe/Paris" {
		t.Errorf("zones %v", zones)
	}
}

// The actions the daemon asks polkit about must be the ones the shipped
// policy declares: polkit refuses an action it does not know, so a typo
// in either place would refuse every request with no clue why.
func TestPolicyDeclaresEveryActionAsked(t *testing.T) {
	b, err := os.ReadFile("../../data/polkit-1/actions/org.freedesktop.timedate1.policy")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`<action id="([^"]+)">`).FindAllStringSubmatch(string(b), -1) {
		declared[m[1]] = true
	}
	w := newWorld(t)
	w.ntpSvc = "chronyd"
	w.s.SetTimezone(":1.9", "Europe/Paris", true)
	w.s.SetLocalRTC(":1.9", true, false, true)
	w.s.SetTime(":1.9", 60*1e6, true, true)
	w.s.SetNTP(":1.9", true, true)
	asked := map[string]bool{}
	for _, a := range w.asked {
		asked[a] = true
		if !declared[a] {
			t.Errorf("daemon asks %s, which the policy does not declare", a)
		}
	}
	for a := range declared {
		if !asked[a] {
			t.Errorf("policy declares %s, which the daemon never asks", a)
		}
	}
}
