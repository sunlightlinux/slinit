package main

import (
	"math"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/dbusservice"
	"github.com/sunlightlinux/slinit/pkg/polkit"
	"github.com/sunlightlinux/slinit/pkg/timedate"
)

const (
	busName = "org.freedesktop.timedate1"
	objPath = dbus.ObjectPath("/org/freedesktop/timedate1")
	iface   = "org.freedesktop.timedate1"
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
	mu  sync.Mutex // one change at a time, as timedated serialises them
	svc *dbusservice.Service
	pk  *polkit.Checker
	m   machine
}

// newServer wires the object to its D-Bus scaffolding.
func newServer(conn *dbus.Conn, pk *polkit.Checker, m machine) *server {
	s := &server{pk: pk, m: m}
	s.svc = &dbusservice.Service{
		Name: "slinit-timedated", Conn: conn, Path: objPath, Iface: iface,
		Props: properties, Get: s.property, Now: func() time.Time { return s.m.now() },
	}
	return s
}

var properties = []dbusservice.Property{
	{Name: "Timezone", Sig: "s", Emits: true},
	{Name: "LocalRTC", Sig: "b", Emits: true},
	{Name: "CanNTP", Sig: "b"},
	{Name: "NTP", Sig: "b", Emits: true},
	{Name: "NTPSynchronized", Sig: "b"},
	{Name: "TimeUSec", Sig: "t"},
	{Name: "RTCTimeUSec", Sig: "t"},
}

func oneZero(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// SetTimezone points the system at a new time zone.
func (s *server) SetTimezone(sender dbus.Sender, zone string, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
	if err := timedate.ValidateZone(zone); err != nil {
		return dbusservice.InvalidArgs("Invalid or not installed time zone '%s'", zone)
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
		return dbusservice.Failed("Failed to set time zone: %v", err)
	}
	if loc, err := time.LoadLocation(zone); err == nil {
		time.Local = loc // our own tzset()
	}
	s.m.kernelTZ()
	if timedate.RTCInLocal() {
		// The RTC holds local time, which just changed meaning.
		if err := s.m.rtcFromSystem(true); err != nil {
			s.svc.Logf("failed to sync time to hardware clock, ignoring: %v", err)
		}
	}
	s.svc.Logf("Changed time zone to '%s'.", zone)
	s.svc.Emit("Timezone")
	return nil
}

// SetLocalRTC chooses whether the RTC keeps local time or UTC, and moves
// either the system clock or the RTC to match.
func (s *server) SetLocalRTC(sender dbus.Sender, localRTC, fixSystem, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
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
		return dbusservice.Failed("Failed to set RTC to %s: %v", map[bool]string{true: "local", false: "UTC"}[localRTC], err)
	}
	if fixSystem {
		if err := s.m.systemFromRTC(localRTC); err != nil {
			s.svc.Logf("failed to set system clock from RTC, ignoring: %v", err)
		}
	} else if err := s.m.rtcFromSystem(localRTC); err != nil {
		s.svc.Logf("failed to sync time to hardware clock, ignoring: %v", err)
	}
	s.svc.Logf("RTC configured to %s time.", map[bool]string{true: "local", false: "UTC"}[localRTC])
	s.svc.Emit("LocalRTC")
	return nil
}

// SetTime sets the system clock, absolutely or relative to now, in
// microseconds.
func (s *server) SetTime(sender dbus.Sender, usec int64, relative, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
	if svc := s.m.findNTP(); svc != "" && s.m.ntpRunning(svc) {
		return dbus.NewError(iface+".AutomaticTimeSyncEnabled", []any{"Automatic time synchronization is enabled"})
	}
	if !relative && usec <= 0 {
		return dbusservice.InvalidArgs("Invalid absolute time")
	}
	if relative && usec == 0 {
		return nil
	}
	var t time.Time
	if relative {
		now := s.m.now()
		nowUsec := now.UnixMicro()
		if (usec > 0 && nowUsec > math.MaxInt64-usec) || (usec < 0 && nowUsec < math.MinInt64-usec) {
			return dbusservice.InvalidArgs("Time value overflow")
		}
		t = time.UnixMicro(nowUsec + usec)
	} else {
		t = time.UnixMicro(usec)
	}
	if t.Before(timeFloor) {
		return dbusservice.InvalidArgs("Requested to set the clock to time before build time, refusing.")
	}
	if err := s.pk.Authorize(sender, iface+".set-time", nil, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.m.setSystemTime(t); err != nil {
		return dbusservice.Failed("Failed to set local time: %v", err)
	}
	if err := s.m.rtcFromSystem(timedate.RTCInLocal()); err != nil {
		s.svc.Logf("failed to sync time to hardware clock, ignoring: %v", err)
	}
	s.svc.Logf("Changed local time to %s", t.Local().Format("Mon 2006-01-02 15:04:05 MST"))
	return nil
}

// SetNTP turns the time-sync service on or off.
func (s *server) SetNTP(sender dbus.Sender, useNTP, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
	if s.m.findNTP() == "" {
		return dbus.NewError(iface+".NoNTPSupport", []any{"NTP not supported"})
	}
	if err := s.pk.Authorize(sender, iface+".set-ntp", map[string]string{"enable": oneZero(useNTP)}, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.m.setNTP(useNTP); err != nil {
		return dbusservice.Failed("%v", err)
	}
	s.svc.Logf("Set NTP to %s.", map[bool]string{true: "enabled", false: "disabled"}[useNTP])
	s.svc.Emit("NTP")
	return nil
}

// ListTimezones lists the installed time zones. It needs no authorization.
func (s *server) ListTimezones() ([]string, *dbus.Error) {
	defer s.svc.Touch()()
	zones, err := timedate.ListTimezones()
	if err != nil {
		return nil, dbusservice.Failed("Failed to read list of time zones: %v", err)
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

// setKernelTimezone tells the kernel the offset of the new local time
// (settimeofday with only the timezone), which FAT timestamps and a
// local-time RTC rely on; a failure is ignored, as timedated ignores it.
func setKernelTimezone() {
	_, off := time.Now().Zone()
	tz := struct{ minuteswest, dsttime int32 }{int32(-off / 60), 0}
	_, _, _ = syscall.Syscall(syscall.SYS_SETTIMEOFDAY, 0, uintptr(unsafe.Pointer(&tz)), 0)
}
