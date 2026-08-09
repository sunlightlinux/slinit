// status.go — collect + render the "timedatectl status" output.
//
// Three output flavors:
//   - text (default) — systemd-style two-column table
//   - show           — `show` subcommand, KEY=VALUE dump
//   - json           — machine-readable, systemd-compatible field names
//
// Data sources (all on-disk, no D-Bus):
//   - Local / Universal time: time.Now() with the running Local
//   - RTC time: /dev/rtc via RTC_RD_TIME (rtc.go)
//   - Time zone: readlink(/etc/localtime) → suffix under zoneinfo
//   - RTC-in-local: /etc/adjtime line 3
//   - NTP service: detectNTPService() walks known service names

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type statusFields struct {
	Timezone              string    `json:"Timezone,omitempty"`
	LocalTime             time.Time `json:"LocalTime,omitempty"`
	UniversalTime         time.Time `json:"UniversalTime,omitempty"`
	RTCTime               time.Time `json:"RTCTime,omitempty"`
	RTCTimeValid          bool      `json:"-"`
	RTCInLocalTZ          bool      `json:"RTCInLocalTZ"`
	NTP                   string    `json:"NTP,omitempty"`
	NTPService            string    `json:"NTPService,omitempty"`
	NTPServiceRunning     bool      `json:"NTPSynchronized"`
	CanNTP                bool      `json:"CanNTP"`
	SystemClockSynchronized bool    `json:"SystemClockSynchronized"`
}

func runStatus(out io.Writer, opts options, mode string) error {
	s := collectStatus()
	if opts.jsonMode != "" && opts.jsonMode != "off" {
		return renderJSON(out, s, opts.jsonMode)
	}
	if mode == "show" {
		return renderShow(out, s)
	}
	return renderText(out, s)
}

func collectStatus() statusFields {
	var s statusFields
	now := time.Now()
	s.LocalTime = now.In(time.Local)
	s.UniversalTime = now.UTC()
	s.Timezone = currentZoneName()
	s.RTCInLocalTZ = rtcInLocal()
	if rt, err := readRTCTime(s.RTCInLocalTZ); err == nil && rt != nil {
		s.RTCTime = *rt
		s.RTCTimeValid = true
	}
	svc, running := detectNTPService()
	if svc != "" {
		s.NTPService = svc
		s.NTPServiceRunning = running
		s.CanNTP = true
		if running {
			s.NTP = "active"
		} else {
			s.NTP = "inactive"
		}
	} else {
		s.NTP = "n/a"
	}
	// slinit does not maintain a discipline-quality "sync" flag; we
	// treat "NTP daemon running" as the best proxy — matches what
	// operators expect when reading the field.
	s.SystemClockSynchronized = s.NTPServiceRunning
	return s
}

// renderText mirrors systemd's two-column layout. Field widths match
// what timedatectl(1) uses so aligned diff-based tests / dashboards
// aren't broken by cosmetic drift.
func renderText(out io.Writer, s statusFields) error {
	tf := func(t time.Time) string {
		return t.Format("Mon 2006-01-02 15:04:05 MST")
	}
	fmt.Fprintf(out, "%25s: %s\n", "Local time", tf(s.LocalTime))
	fmt.Fprintf(out, "%25s: %s\n", "Universal time", tf(s.UniversalTime.In(time.UTC)))
	if s.RTCTimeValid {
		fmt.Fprintf(out, "%25s: %s\n", "RTC time",
			s.RTCTime.Format("Mon 2006-01-02 15:04:05"))
	} else {
		fmt.Fprintf(out, "%25s: %s\n", "RTC time", "n/a")
	}
	if s.Timezone != "" {
		_, offset := s.LocalTime.Zone()
		hrs := offset / 3600
		mins := (offset % 3600) / 60
		if mins < 0 {
			mins = -mins
		}
		fmt.Fprintf(out, "%25s: %s (%s, %+03d%02d)\n", "Time zone",
			s.Timezone, s.LocalTime.Format("MST"), hrs, mins)
	} else {
		fmt.Fprintf(out, "%25s: %s\n", "Time zone", "n/a")
	}
	fmt.Fprintf(out, "%25s: %s\n", "System clock synchronized", yesNo(s.SystemClockSynchronized))
	if s.NTPService != "" {
		fmt.Fprintf(out, "%25s: %s (%s)\n", "NTP service", s.NTPService, s.NTP)
	} else {
		fmt.Fprintf(out, "%25s: %s\n", "NTP service", "n/a")
	}
	fmt.Fprintf(out, "%25s: %s\n", "RTC in local TZ", yesNo(s.RTCInLocalTZ))
	return nil
}

// renderShow implements the `show` subcommand: KEY=VALUE lines with
// booleans as `yes`/`no` and timestamps as microseconds-since-epoch,
// matching systemd's D-Bus property dump.
func renderShow(out io.Writer, s statusFields) error {
	fmt.Fprintf(out, "Timezone=%s\n", s.Timezone)
	fmt.Fprintf(out, "LocalRTC=%s\n", yesNo(s.RTCInLocalTZ))
	fmt.Fprintf(out, "CanNTP=%s\n", yesNo(s.CanNTP))
	fmt.Fprintf(out, "NTP=%s\n", yesNo(s.NTPServiceRunning))
	fmt.Fprintf(out, "NTPSynchronized=%s\n", yesNo(s.SystemClockSynchronized))
	fmt.Fprintf(out, "TimeUSec=%d\n", s.LocalTime.UnixMicro())
	if s.RTCTimeValid {
		fmt.Fprintf(out, "RTCTimeUSec=%d\n", s.RTCTime.UnixMicro())
	}
	if s.NTPService != "" {
		fmt.Fprintf(out, "NTPService=%s\n", s.NTPService)
	}
	return nil
}

func renderJSON(out io.Writer, s statusFields, mode string) error {
	enc := json.NewEncoder(out)
	if mode == "pretty" {
		enc.SetIndent("", "    ")
	}
	return enc.Encode(s)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
