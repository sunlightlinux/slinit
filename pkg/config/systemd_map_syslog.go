package config

import (
	"fmt"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// SyslogIdentifier=, SyslogLevel=, SyslogLevelPrefix= and
// LogExtraFields= map onto the settings of the same meaning added in 4.1
// (syslog-identifier, syslog-level, syslog-level-prefix,
// log-extra-fields). They apply to output that reaches the journal,
// which a converted process service's does (log-type = journal, see
// defaultToJournal). An empty assignment resets, as in systemd.

func init() {
	ident := exScalar("syslog-identifier", func(v string) (string, []SystemdWarning, error) { return v, nil, nil })
	registerService("SyslogIdentifier", sdRule{custom: func(cfg *SystemdConfig, key, val string) []SystemdWarning {
		// config_parse_unit_string_printf: specifiers are expanded.
		v, warns := expandSpecifiers(cfg, key, strings.TrimSpace(val))
		return append(warns, ident.custom(cfg, key, v)...)
	}})
	registerService("SyslogLevel", exScalar("syslog-level", syslogLevelConv))
	registerService("SyslogLevelPrefix", exScalar("syslog-level-prefix", func(v string) (string, []SystemdWarning, error) {
		b, err := exBool(v)
		return b, nil, err
	}))
	registerService("LogExtraFields", sdRule{custom: mapLogExtraFields})
}

// syslogLevelConv takes systemd's log level names (log_level_from_string:
// emerg, alert, crit, err, warning, notice, info, debug) or 0..7.
func syslogLevelConv(v string) (string, []SystemdWarning, error) {
	switch v {
	case "emerg", "alert", "crit", "err", "warning", "notice", "info", "debug",
		"0", "1", "2", "3", "4", "5", "6", "7":
		return v, nil, nil
	}
	return "", nil, fmt.Errorf("%q is not a log level", v)
}

// mapLogExtraFields adds the unit's FIELD=VALUE words. systemd takes
// quoted words, so a value may hold blanks; slinit's list is split on
// blanks, so such a field is dropped with a warning. A field name the
// journal refuses is dropped too, as systemd refuses it.
func mapLogExtraFields(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(val)
	if v == "" {
		cfg.dropMapped("log-extra-fields")
		return nil
	}
	var warns []SystemdWarning
	var kept []string
	rest := v
	for {
		w, r, found, err := extractSystemdWord(rest, false)
		if err != nil {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s=%s: %v — ignored", key, val, err)})
			break
		}
		if !found {
			break
		}
		rest = r
		name, value, ok := strings.Cut(w, "=")
		switch {
		case !ok || !journal.IsValidFieldName(name) || strings.HasPrefix(name, "_"):
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %q is not FIELD=VALUE with a journal field name — ignored", key, w)})
		case strings.ContainsAny(value, " \t\n"):
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %q holds blanks, which log-extra-fields cannot — ignored", key, w)})
		default:
			kept = append(kept, w)
		}
	}
	if len(kept) > 0 {
		cfg.appendMapped("log-extra-fields", strings.Join(kept, " "))
	}
	return warns
}
