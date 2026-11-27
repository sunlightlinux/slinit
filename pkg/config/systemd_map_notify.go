package config

import (
	"strings"
)

// Type=notify and Type=notify-reload map onto ready-notification =
// sd-notify: the service is started when it sends READY=1 on
// $NOTIFY_SOCKET, as in systemd. Before slinit had that mode a notify
// unit was a plain process service, STARTED at the fork, so dependents
// started before it was ready.
//
// WatchdogSec= maps onto watchdog-timeout for such a service, which then
// gets $WATCHDOG_USEC and is kept alive by WATCHDOG=1. For any other
// type there is no channel for the keepalives, and slinit refuses a
// watchdog without one, so WatchdogSec= stays a gap there.
//
// Both depend on Type=, which may come anywhere in the unit or in a
// drop-in, so they are decided in a finisher.

type sdNotifyState struct {
	watchdog    string // WatchdogSec= in seconds, "" for none
	watchdogRaw string
}

func sdNotifyStateOf(cfg *SystemdConfig) *sdNotifyState {
	return sdState(cfg, "notify", func() *sdNotifyState { return &sdNotifyState{} })
}

func init() {
	registerFinisher(sdNotifyFinish)
}

// mapWatchdogSec records WatchdogSec=. 0 and infinity turn the watchdog
// off, as does an empty value.
func mapWatchdogSec(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := sdNotifyStateOf(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	st.watchdog, st.watchdogRaw = "", val
	if v == "" || v == "infinity" {
		return nil
	}
	s, ok := parseSystemdSeconds(v, "")
	if !ok {
		return []SystemdWarning{{"WARN", "[Service] WatchdogSec=" + val + " is no time span — ignored, as systemd does"}}
	}
	if s != "0" {
		st.watchdog = s
	}
	return nil
}

func sdNotifyFinish(cfg *SystemdConfig) []SystemdWarning {
	st := sdNotifyStateOf(cfg)
	notify := cfg.unitType == "notify" || cfg.unitType == "notify-reload"
	if notify {
		if v, ok := cfg.hasMapped("notify-access"); ok && v == "none" {
			// NotifyAccess=none: systemd ignores READY=1 too, and slinit
			// disables readiness for it; nothing to wire.
			notify = false
		} else {
			cfg.setMapped("ready-notification", "sd-notify")
		}
	}
	if st.watchdog == "" {
		return nil
	}
	if !notify {
		return gapWarning("Service", "WatchdogSec", st.watchdogRaw, lcWatchdogGap)
	}
	cfg.setMapped("watchdog-timeout", st.watchdog)
	return nil
}
