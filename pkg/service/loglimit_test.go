package service

import (
	"testing"
	"time"
)

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"", -1, true},
		{"off", -1, true},
		{"none", -1, true},
		{"any", -1, true},
		{"emerg", 0, true},
		{"alert", 1, true},
		{"crit", 2, true},
		{"err", 3, true},
		{"warn", 4, true},
		{"notice", 5, true},
		{"info", 6, true},
		{"debug", 7, true},
		{"5", 5, true},
		{"INFO", 6, true},
		{"  warning  ", 4, true},
		{"bogus", -1, false},
		{"8", -1, false},
	}
	for _, c := range cases {
		got, err := ParseLogLevel(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%q: ok=%v want %v err=%v", c.in, err == nil, c.ok, err)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("%q: got %d want %d", c.in, got, c.want)
		}
	}
}

func TestExtractSyslogLevel(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		// RFC 5424 <PRI> = facility*8 + level. Masked to 7 → level.
		{"<11>Mar 1 12:34:56 hello", 3}, // facility=1 (user), level=3 (err)
		{"<6>info line", 6},
		{"<7>debug", 7},
		{"<0>emergency", 0},
		{"<165>full priority", 5}, // 165 & 7 = 5

		// Uppercase keyword prefix — the common convention emitted
		// by Go log.Printf / Python logging / java.util.logging.
		{"EMERG: outage", 0},
		{"ALERT: paged", 1},
		{"CRIT: db down", 2},
		{"ERR: something broke", 3},
		{"ERROR: connection refused", 3},
		{"WARN: slow query", 4},
		{"WARNING: retry", 4},
		{"NOTICE: config reloaded", 5},
		{"INFO: request handled", 6},
		{"DEBUG: entering fn", 7},

		// Malformed / prose that must NOT trigger keyword match.
		{"info: lowercase does not count", 6},
		{"INFOno colon here", 6},
		{"the ERROR message is", 6},   // not at start
		{"plain text line", 6},        // no prefix → info default
		{"<no-num>not numeric", 6}, // malformed <> → keyword fallback → info
		{"<>", 6},                  // empty digits → keyword fallback → info
		{"", 6},
	}
	for _, c := range cases {
		if got := extractSyslogLevel([]byte(c.line)); got != c.want {
			t.Errorf("%q: got %d want %d", c.line, got, c.want)
		}
	}
}

func TestLogRotatorRateLimit(t *testing.T) {
	cfg := LogRotatorConfig{
		ServiceName:  "test",
		RateInterval: 60 * time.Second,
		RateBurst:    3,
		LogLevelMax:  -1,
	}
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Three should pass; the fourth should be denied.
	for i := 0; i < 3; i++ {
		if !lr.tryConsumeRateToken() {
			t.Errorf("token %d: should have passed", i)
		}
	}
	if lr.tryConsumeRateToken() {
		t.Error("4th token: should have been denied (bucket empty)")
	}
}

func TestLogRotatorRateLimitRefill(t *testing.T) {
	cfg := LogRotatorConfig{
		ServiceName:  "test",
		RateInterval: 100 * time.Millisecond,
		RateBurst:    2,
		LogLevelMax:  -1,
	}
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Drain.
	lr.tryConsumeRateToken()
	lr.tryConsumeRateToken()
	if lr.tryConsumeRateToken() {
		t.Fatal("drain: 3rd should have been denied")
	}
	// Wait long enough for full refill.
	time.Sleep(120 * time.Millisecond)
	if !lr.tryConsumeRateToken() {
		t.Error("after refill: should have accepted")
	}
}
