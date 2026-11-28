package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	"FixedRandomDelay": true, "AccuracySec": true,
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
	var conds [][2]string // the timer's [Unit] Condition*= / Assert*=
	var warns []SystemdWarning
	persistent := false
	delay := ""
	var bootSec, activeSec time.Duration
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
			if m != nil && section == "Unit" && (strings.HasPrefix(m[1], "Condition") || strings.HasPrefix(m[1], "Assert")) {
				if strings.TrimSpace(trimQuotes(strings.TrimSpace(m[2]))) == "" {
					// An empty one empties the timer's list of its kind
					// (conditions or assertions), a drop-in's included.
					prefix := "Condition"
					if strings.HasPrefix(m[1], "Assert") {
						prefix = "Assert"
					}
					kept := conds[:0]
					for _, c := range conds {
						if !strings.HasPrefix(c[0], prefix) {
							kept = append(kept, c)
						}
					}
					conds = kept
					continue
				}
				conds = append(conds, [2]string{m[1], strings.TrimSpace(m[2])})
				continue
			}
			if m == nil || section != "Timer" {
				continue
			}
			key, val := m[1], strings.TrimSpace(trimQuotes(strings.TrimSpace(m[2])))
			switch {
			case (key == "OnCalendar" || sdTimerMonotonic[key]) && val == "":
				// An empty one resets the timer's list, calendar and
				// monotonic times alike.
				cals, bootSec, activeSec = nil, 0, 0
			case key == "OnCalendar":
				cals = append(cals, val)
			case key == "OnBootSec" || key == "OnStartupSec" || key == "OnUnitActiveSec":
				// OnStartupSec= counts from the manager's start, which
				// for PID 1 is the boot: start-on-boot-sec.
				us, ok := sdTimespanUsec(val)
				if !ok {
					warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %s=%s is no time span; ignored", name, key, val)})
					break
				}
				d := &bootSec
				if key == "OnUnitActiveSec" {
					d = &activeSec
				}
				// Several: the earliest elapses first; slinit keeps one.
				// 0 elapses at once: a millisecond for the boot time, as 0
				// means unset in slinit. An interval after each start
				// below a second would rerun a job without pause: a
				// second at least.
				v := max(time.Duration(us)*time.Microsecond, time.Millisecond)
				if key == "OnUnitActiveSec" {
					v = max(v, time.Second)
				}
				if *d == 0 || v < *d {
					*d = v
				}
			case key == "Persistent":
				persistent = truthy(val)
			case key == "RandomizedDelaySec":
				delay = ""
				if us, ok := sdTimespanUsec(val); ok && us > 0 {
					delay = (time.Duration(us) * time.Microsecond).String()
				} else if !ok {
					warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: RandomizedDelaySec=%s is no time span; ignored", name, val)})
				}
			case key == "Unit":
				if strings.TrimSuffix(val, ".service") != cfg.svcName {
					return []SystemdWarning{{"NOTE", fmt.Sprintf("%s starts %s, not this service: not read", name, val)}}
				}
			case sdTimerMonotonic[key]:
				warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
					"%s: %s=%s not carried: slinit's timers count from the boot and from the service's last start only", name, key, val)})
			case sdTimerNotCarried[key] && val != "":
				warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s: %s=%s not carried", name, key, val)})
			}
		}
	}
	if len(cals) > 0 {
		if _, err := service.ParseCalendar(cals[0]); err != nil {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
				"%s: OnCalendar=%s is no calendar expression slinit reads (%v) — not carried", name, cals[0], err)})
			cals = nil
		}
	}
	if len(cals) == 0 && bootSec == 0 && activeSec == 0 {
		return warns
	}
	var what []string
	if len(cals) > 0 {
		if len(cals) > 1 {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
				"%s: slinit's start-on-calendar takes one expression, so OnCalendar=%s is kept and %s not carried",
				name, cals[0], strings.Join(cals[1:], ", "))})
		}
		cfg.setMapped("start-on-calendar", cals[0])
		if persistent {
			cfg.setMapped("start-on-calendar-persistent", "yes")
		}
		what = append(what, "OnCalendar="+cals[0])
	}
	if bootSec > 0 {
		cfg.setMapped("start-on-boot-sec", bootSec.String())
		what = append(what, "start-on-boot-sec = "+bootSec.String())
	}
	if activeSec > 0 {
		cfg.setMapped("start-on-unit-active-sec", activeSec.String())
		what = append(what, "start-on-unit-active-sec = "+activeSec.String())
	}
	if delay != "" {
		cfg.setMapped("start-on-calendar-randomized-delay", delay)
	}
	if cfg.installed {
		// Wanted by a target too: systemd starts it at boot as well as
		// at the times, and so may slinit.
		for _, c := range conds {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
				"%s: %s=%s not carried: it gates the timer, and this service also starts at boot", name, c[0], c[1])})
		}
		return append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"%s read: started at %s, and at boot when enabled, as its [Install] asks", name, strings.Join(what, ", "))})
	}
	// Nothing starts it but the timer, in systemd. manual = yes keeps
	// it so when an enable, or a unit naming it, loads it; a hard
	// dependency on it then waits for the calendar, where systemd would
	// start it: said, since that is a unit's choice.
	cfg.setMapped("manual", "yes")
	// The timer's conditions gate the only thing that starts it, so they
	// become the service's, ANDed with its own (fstrim.timer:
	// ConditionVirtualization=!container). Not an empty one, which would
	// reset the service's own list, nor a triggering one, whose "one of"
	// would join the service's.
	for _, c := range conds {
		v := strings.TrimSpace(trimQuotes(c[1]))
		if v == "" || strings.HasPrefix(v, "|") {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
				"%s: %s=%s not carried to the service: an empty or triggering (|) check of the timer cannot join the service's own", name, c[0], c[1])})
			continue
		}
		warns = append(warns, applyUnitKey(cfg, c[0], c[1])...)
	}
	return append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
		"%s read: started at %s, with manual = yes so the timer alone starts it "+
			"(a service that depends on it does not start it; drop manual if one should)", name, strings.Join(what, ", "))})
}
