package main

import (
	"fmt"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/polkit"
	"github.com/sunlightlinux/slinit/pkg/timedate"
)

const (
	busName   = "org.freedesktop.timedate1"
	objPath   = dbus.ObjectPath("/org/freedesktop/timedate1")
	iface     = "org.freedesktop.timedate1"
	propIface = "org.freedesktop.DBus.Properties"
)

// timeFloor refuses a clock set before it, as systemd-timedated refuses one
// before its build time: a date earlier than this release is certainly
// wrong, and accepting it would break TLS and every timestamp after.
var timeFloor = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

// machine is everything the daemon does to the system, so tests can run
// the whole method logic without touching the clock, the RTC or services.
type machine struct {
	setSystemTime func(time.Time) error
	rtcFromSystem func(inLocal bool) error // hwclock --systohc
	systemFromRTC func(inLocal bool) error // hwclock --hctosys
	readRTC       func() (time.Time, bool) // RTC fields read as UTC
	findNTP       func() string
	ntpRunning    func(svc string) bool
	setNTP        func(bool) error
	ntpSynced     func() bool
	kernelTZ      func()
	now           func() time.Time
}

func realMachine() machine {
	return machine{
		setSystemTime: timedate.SetSystemTime,
		rtcFromSystem: timedate.WriteRTCFromSystem,
		systemFromRTC: timedate.SystemFromRTC,
		readRTC: func() (time.Time, bool) {
			t, err := timedate.ReadRTC(false)
			if err != nil || t == nil {
				return time.Time{}, false
			}
			return *t, true
		},
		findNTP:    timedate.FindNTPService,
		ntpRunning: timedate.ServiceRunning,
		setNTP:     timedate.SetNTP,
		ntpSynced:  timedate.NTPSynchronized,
		kernelTZ:   setKernelTimezone,
		now:        time.Now,
	}
}

type server struct {
	mu   sync.Mutex // one change at a time, as timedated serialises them
	conn *dbus.Conn
	pk   *polkit.Checker
	m    machine

	inflight atomic.Int32
	lastUse  atomic.Int64 // unix nanoseconds
}

func (s *server) touch() func() {
	s.inflight.Add(1)
	s.lastUse.Store(s.m.now().UnixNano())
	return func() {
		s.lastUse.Store(s.m.now().UnixNano())
		s.inflight.Add(-1)
	}
}

func invalidArgs(msg string) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{msg})
}

func failed(format string, a ...any) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.Failed", []any{fmt.Sprintf(format, a...)})
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "slinit-timedated: "+format+"\n", a...)
}

func oneZero(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// emit sends PropertiesChanged for the named properties with their current
// values, as sd_bus_emit_properties_changed does for EMITS_CHANGE ones.
func (s *server) emit(names ...string) {
	if s.conn == nil {
		return
	}
	changed := map[string]dbus.Variant{}
	for _, n := range names {
		if v, ok := s.property(n); ok {
			changed[n] = v
		}
	}
	_ = s.conn.Emit(objPath, propIface+".PropertiesChanged", iface, changed, []string{})
}

// SetTimezone points the system at a new time zone.
func (s *server) SetTimezone(sender dbus.Sender, zone string, interactive bool) *dbus.Error {
	defer s.touch()()
	if err := timedate.ValidateZone(zone); err != nil {
		return invalidArgs(fmt.Sprintf("Invalid or not installed time zone '%s'", zone))
	}
	if zone == timedate.CurrentZone() {
		return nil
	}
	if err := s.pk.Authorize(sender, iface+".set-timezone", map[string]string{"zone": zone}, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := timedate.SetTimezone(zone); err != nil {
		return failed("Failed to set time zone: %v", err)
	}
	if loc, err := time.LoadLocation(zone); err == nil {
		time.Local = loc // our own tzset()
	}
	s.m.kernelTZ()
	if timedate.RTCInLocal() {
		// The RTC holds local time, which just changed meaning.
		if err := s.m.rtcFromSystem(true); err != nil {
			logf("failed to sync time to hardware clock, ignoring: %v", err)
		}
	}
	logf("Changed time zone to '%s'.", zone)
	s.emit("Timezone")
	return nil
}

// SetLocalRTC chooses whether the RTC keeps local time or UTC, and moves
// either the system clock or the RTC to match.
func (s *server) SetLocalRTC(sender dbus.Sender, localRTC, fixSystem, interactive bool) *dbus.Error {
	defer s.touch()()
	if localRTC == timedate.RTCInLocal() && !fixSystem {
		return nil
	}
	if err := s.pk.Authorize(sender, iface+".set-local-rtc", map[string]string{"local_rtc": oneZero(localRTC)}, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mode := "UTC"
	if localRTC {
		mode = "LOCAL"
	}
	if err := timedate.WriteAdjtimeMode(mode); err != nil {
		return failed("Failed to set RTC to %s: %v", map[bool]string{true: "local", false: "UTC"}[localRTC], err)
	}
	if fixSystem {
		if err := s.m.systemFromRTC(localRTC); err != nil {
			logf("failed to set system clock from RTC, ignoring: %v", err)
		}
	} else if err := s.m.rtcFromSystem(localRTC); err != nil {
		logf("failed to sync time to hardware clock, ignoring: %v", err)
	}
	logf("RTC configured to %s time.", map[bool]string{true: "local", false: "UTC"}[localRTC])
	s.emit("LocalRTC")
	return nil
}

// SetTime sets the system clock, absolutely or relative to now, in
// microseconds.
func (s *server) SetTime(sender dbus.Sender, usec int64, relative, interactive bool) *dbus.Error {
	defer s.touch()()
	if svc := s.m.findNTP(); svc != "" && s.m.ntpRunning(svc) {
		return dbus.NewError(iface+".AutomaticTimeSyncEnabled", []any{"Automatic time synchronization is enabled"})
	}
	if !relative && usec <= 0 {
		return invalidArgs("Invalid absolute time")
	}
	if relative && usec == 0 {
		return nil
	}
	var t time.Time
	if relative {
		now := s.m.now()
		nowUsec := now.UnixMicro()
		if (usec > 0 && nowUsec > math.MaxInt64-usec) || (usec < 0 && nowUsec < math.MinInt64-usec) {
			return invalidArgs("Time value overflow")
		}
		t = time.UnixMicro(nowUsec + usec)
	} else {
		t = time.UnixMicro(usec)
	}
	if t.Before(timeFloor) {
		return invalidArgs("Requested to set the clock to time before build time, refusing.")
	}
	if err := s.pk.Authorize(sender, iface+".set-time", nil, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.m.setSystemTime(t); err != nil {
		return failed("Failed to set local time: %v", err)
	}
	if err := s.m.rtcFromSystem(timedate.RTCInLocal()); err != nil {
		logf("failed to sync time to hardware clock, ignoring: %v", err)
	}
	logf("Changed local time to %s", t.Local().Format("Mon 2006-01-02 15:04:05 MST"))
	return nil
}

// SetNTP turns the time-sync service on or off.
func (s *server) SetNTP(sender dbus.Sender, useNTP, interactive bool) *dbus.Error {
	defer s.touch()()
	if s.m.findNTP() == "" {
		return dbus.NewError(iface+".NoNTPSupport", []any{"NTP not supported"})
	}
	if err := s.pk.Authorize(sender, iface+".set-ntp", map[string]string{"enable": oneZero(useNTP)}, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.m.setNTP(useNTP); err != nil {
		return failed("%v", err)
	}
	logf("Set NTP to %s.", map[bool]string{true: "enabled", false: "disabled"}[useNTP])
	s.emit("NTP")
	return nil
}

// ListTimezones lists the installed time zones. It needs no authorization.
func (s *server) ListTimezones() ([]string, *dbus.Error) {
	defer s.touch()()
	zones, err := timedate.ListTimezones()
	if err != nil {
		return nil, failed("Failed to read list of time zones: %v", err)
	}
	return zones, nil
}

// property returns one of the interface's properties, computed when asked,
// as timedated computes TimeUSec, RTCTimeUSec and NTPSynchronized.
func (s *server) property(name string) (dbus.Variant, bool) {
	switch name {
	case "Timezone":
		return dbus.MakeVariant(timedate.CurrentZone()), true
	case "LocalRTC":
		return dbus.MakeVariant(timedate.RTCInLocal()), true
	case "CanNTP":
		return dbus.MakeVariant(s.m.findNTP() != ""), true
	case "NTP":
		svc := s.m.findNTP()
		return dbus.MakeVariant(svc != "" && s.m.ntpRunning(svc)), true
	case "NTPSynchronized":
		return dbus.MakeVariant(s.m.ntpSynced()), true
	case "TimeUSec":
		return dbus.MakeVariant(uint64(s.m.now().UnixMicro())), true
	case "RTCTimeUSec":
		var us uint64
		if t, ok := s.m.readRTC(); ok {
			us = uint64(t.UnixMicro())
		}
		return dbus.MakeVariant(us), true
	}
	return dbus.Variant{}, false
}

var propertyNames = []string{"Timezone", "LocalRTC", "CanNTP", "NTP", "NTPSynchronized", "TimeUSec", "RTCTimeUSec"}

// props serves org.freedesktop.DBus.Properties for the object; every
// property is read-only, changed only through the methods.
type props struct{ s *server }

func (p props) Get(ifc, name string) (dbus.Variant, *dbus.Error) {
	defer p.s.touch()()
	if ifc != iface {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", []any{ifc})
	}
	v, ok := p.s.property(name)
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", []any{name})
	}
	return v, nil
}

func (p props) GetAll(ifc string) (map[string]dbus.Variant, *dbus.Error) {
	defer p.s.touch()()
	if ifc != iface && ifc != "" {
		return map[string]dbus.Variant{}, nil
	}
	out := map[string]dbus.Variant{}
	for _, n := range propertyNames {
		v, _ := p.s.property(n)
		out[n] = v
	}
	return out, nil
}

func (p props) Set(ifc, name string, _ dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", []any{name})
}

// setKernelTimezone tells the kernel the offset of the new local time
// (settimeofday with only the timezone), which FAT timestamps and a
// local-time RTC rely on; a failure is ignored, as timedated ignores it.
func setKernelTimezone() {
	_, off := time.Now().Zone()
	tz := struct{ minuteswest, dsttime int32 }{int32(-off / 60), 0}
	_, _, _ = syscall.Syscall(syscall.SYS_SETTIMEOFDAY, 0, uintptr(unsafe.Pointer(&tz)), 0)
}
