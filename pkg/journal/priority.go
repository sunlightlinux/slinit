package journal

import (
	"fmt"
	"strconv"
	"strings"
)

// ParsePriorityName resolves a symbolic priority name into a numeric
// value. Accepts the short forms slinit-service(5) documents (err /
// warn / info / debug) plus the systemd long forms (error / warning
// / informational) and numeric strings (0..7). Matching is
// case-insensitive so operator input from CLI flags parses cleanly.
func ParsePriorityName(s string) (Priority, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("journal: empty priority")
	}

	// Numeric form.
	if n, err := strconv.Atoi(s); err == nil {
		p := Priority(n)
		if !p.Valid() {
			return 0, fmt.Errorf("journal: priority %d out of range (0..7)", n)
		}
		return p, nil
	}

	// Symbolic form.
	switch strings.ToLower(s) {
	case "emerg", "emergency":
		return PriorityEmergency, nil
	case "alert":
		return PriorityAlert, nil
	case "crit", "critical":
		return PriorityCritical, nil
	case "err", "error":
		return PriorityError, nil
	case "warn", "warning":
		return PriorityWarning, nil
	case "notice":
		return PriorityNotice, nil
	case "info", "informational":
		return PriorityInfo, nil
	case "debug":
		return PriorityDebug, nil
	}

	return 0, fmt.Errorf("journal: unknown priority %q", s)
}

// StripSyslogPriority parses a leading `<N>` prefix from a log line
// per RFC 5424 (the "PRI" part). If present, returns the priority
// (masked to 0..7 severity — the facility bits above are ignored
// for slinit's purposes) and the remaining message with the prefix
// stripped. If absent, returns (Info, line unchanged) — that's the
// default for lines that don't advertise their own priority.
//
// Handles both:
//   - "<3>message"      → severity 3 (err), payload "message"
//   - "<27>message"     → 27 & 7 = 3 (err), payload "message" — the
//                         upper bits are the syslog facility, which
//                         we don't track since slinit's Unit field
//                         already carries the equivalent context.
//
// Malformed prefixes (missing digits, missing `>`, digits out of
// range) are left in place and the whole line becomes payload.
func StripSyslogPriority(line string) (Priority, string) {
	if len(line) < 3 || line[0] != '<' {
		return PriorityInfo, line
	}
	end := strings.IndexByte(line, '>')
	if end < 0 || end == 1 || end > 4 {
		// no closing '>', empty PRI, or >3 digits — invalid, treat
		// as no prefix at all
		return PriorityInfo, line
	}
	pri, err := strconv.Atoi(line[1:end])
	if err != nil || pri < 0 || pri > 191 {
		// 191 = max valid PRI per RFC 5424 (facility 23 << 3 | sev 7)
		return PriorityInfo, line
	}
	// Severity is the low 3 bits.
	return Priority(pri & 0x7), line[end+1:]
}
