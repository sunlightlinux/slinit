package journal

import "testing"

func TestParsePriorityName(t *testing.T) {
	cases := []struct {
		in      string
		want    Priority
		wantErr bool
	}{
		// Symbolic — short forms
		{"emerg", PriorityEmergency, false},
		{"alert", PriorityAlert, false},
		{"crit", PriorityCritical, false},
		{"err", PriorityError, false},
		{"warn", PriorityWarning, false},
		{"notice", PriorityNotice, false},
		{"info", PriorityInfo, false},
		{"debug", PriorityDebug, false},
		// Symbolic — long forms
		{"emergency", PriorityEmergency, false},
		{"critical", PriorityCritical, false},
		{"error", PriorityError, false},
		{"warning", PriorityWarning, false},
		{"informational", PriorityInfo, false},
		// Case-insensitive
		{"ERR", PriorityError, false},
		{"Warn", PriorityWarning, false},
		{"  info  ", PriorityInfo, false},
		// Numeric
		{"0", PriorityEmergency, false},
		{"3", PriorityError, false},
		{"7", PriorityDebug, false},
		// Errors
		{"", 0, true},
		{"nope", 0, true},
		{"8", 0, true},
		{"-1", 0, true},
	}

	for _, c := range cases {
		got, err := ParsePriorityName(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParsePriorityName(%q) expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePriorityName(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePriorityName(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestStripSyslogPriority(t *testing.T) {
	cases := []struct {
		in       string
		wantPri  Priority
		wantMsg  string
	}{
		// Well-formed severities.
		{"<0>emergency", PriorityEmergency, "emergency"},
		{"<3>error text", PriorityError, "error text"},
		{"<6>info line", PriorityInfo, "info line"},
		{"<7>debug", PriorityDebug, "debug"},
		// Severity + facility encoded together (facility 3 = daemon,
		// severity 3 = err → PRI = 3<<3 | 3 = 27).
		{"<27>daemon error", PriorityError, "daemon error"},
		{"<191>facility23-debug7", PriorityDebug, "facility23-debug7"},
		// No prefix at all — default info.
		{"plain message", PriorityInfo, "plain message"},
		{"", PriorityInfo, ""},
		// Malformed prefixes — keep intact.
		{"<>msg", PriorityInfo, "<>msg"},
		{"<abc>msg", PriorityInfo, "<abc>msg"},
		{"<192>too-high", PriorityInfo, "<192>too-high"},
		{"<3msg", PriorityInfo, "<3msg"},
		{"<>", PriorityInfo, "<>"},
	}

	for _, c := range cases {
		pri, msg := StripSyslogPriority(c.in)
		if pri != c.wantPri || msg != c.wantMsg {
			t.Errorf("StripSyslogPriority(%q) = (%v, %q), want (%v, %q)",
				c.in, pri, msg, c.wantPri, c.wantMsg)
		}
	}
}
