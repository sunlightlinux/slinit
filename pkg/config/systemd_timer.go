package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A service's .timer unit.
//
// systemd starts foo.service at the times foo.timer names. slinit has
// that as an activation directive on the service, start-on-calendar
// (a oneshot finished since the last time is run again), so the loader
// reads foo.timer with foo.service, as it reads foo.socket
// (systemd_socket.go): OnCalendar= becomes start-on-calendar, and the
// service gets manual = yes, so that being loaded as a dependency (from
// an enable) starts nothing and the calendar alone runs it, as the
// timer alone runs a timer-activated unit; not when its [Install] has
// it wanted by a target, as systemd then starts it at boot as well. What slinit's calendar does
// not do (more than one OnCalendar=, Persistent=, RandomizedDelaySec=,
// the monotonic OnBootSec=/OnUnitActiveSec= family) is reported.

var sdTimerNotCarried = map[string]bool{
	"RandomizedDelaySec": true, "FixedRandomDelay": true, "AccuracySec": true,
	"WakeSystem": true, "RemainAfterElapse": true, "OnClockChange": true, "OnTimezoneChange": true,
	"RandomizedOffsetSec": true, "DeferReactivation": true,
}

var sdTimerMonotonic = map[string]bool{
	"OnActiveSec": true, "OnBootSec": true, "OnStartupSec": true, "OnUnitActiveSec": true, "OnUnitInactiveSec": true,
}

// applySystemdTimer maps the service's foo.timer onto start-on-calendar.
func applySystemdTimer(cfg *SystemdConfig, unitDirs []string) []SystemdWarning {
	dirs := append(append([]string(nil), unitDirs...), filepath.Dir(cfg.unitPath))
	name := cfg.svcName + ".timer"
	path := firstUnitEntry(name, dirs)
	if path == "" || systemdUnitMasked(path) {
		return nil
	}
	var cals []string
	var warns []SystemdWarning
	persistent := false
	for _, f := range append([]string{path}, systemdDropins(unitDirs, name)...) {
		b, err := os.ReadFile(f)
		if err != nil {
			return []SystemdWarning{{"WARN", fmt.Sprintf("%s: %v — not read", name, err)}}
		}
		section := ""
		for _, raw := range joinSystemdLines(string(b)) {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if m := sectionRe.FindStringSubmatch(line); m != nil {
				section = m[1]
				continue
			}
			m := kvRe.FindStringSubmatch(line)
			if m == nil || section != "Timer" {
				continue
			}
			key, val := m[1], strings.TrimSpace(trimQuotes(strings.TrimSpace(m[2])))
			switch {
			case key == "OnCalendar":
				if val == "" {
					cals = nil // an empty assignment resets the list
				} else {
					cals = append(cals, val)
				}
			case key == "Persistent":
				persistent = truthy(val)
			case key == "Unit":
				if strings.TrimSuffix(val, ".service") != cfg.svcName {
					return []SystemdWarning{{"NOTE", fmt.Sprintf("%s starts %s, not this service: not read", name, val)}}
				}
			case sdTimerMonotonic[key] && val != "":
				warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
					"%s: %s=%s not carried: slinit's start-on-calendar takes calendar times only", name, key, val)})
			case sdTimerNotCarried[key] && val != "":
				warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s: %s=%s not carried", name, key, val)})
			}
		}
	}
	if len(cals) == 0 {
		return warns
	}
	if _, err := service.ParseCalendar(cals[0]); err != nil {
		return append(warns, SystemdWarning{"WARN", fmt.Sprintf(
			"%s: OnCalendar=%s is no calendar expression slinit reads (%v) — the timer is not carried", name, cals[0], err)})
	}
	if len(cals) > 1 {
		warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
			"%s: slinit's start-on-calendar takes one expression, so OnCalendar=%s is kept and %s not carried",
			name, cals[0], strings.Join(cals[1:], ", "))})
	}
	cfg.setMapped("start-on-calendar", cals[0])
	if persistent {
		cfg.setMapped("start-on-calendar-persistent", "yes")
	}
	if cfg.installed {
		// Wanted by a target too: systemd starts it at boot as well as
		// at the times, and so may slinit.
		return append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"%s read: started at OnCalendar=%s (start-on-calendar), and at boot when enabled, as its [Install] asks", name, cals[0])})
	}
	// Nothing starts it but the timer, in systemd. manual = yes keeps
	// it so when an enable, or a unit naming it, loads it; a hard
	// dependency on it then waits for the calendar, where systemd would
	// start it: said, since that is a unit's choice.
	cfg.setMapped("manual", "yes")
	return append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
		"%s read: started at OnCalendar=%s (start-on-calendar), with manual = yes so the calendar alone starts it "+
			"(a service that depends on it does not start it; drop manual if one should)", name, cals[0])})
}
