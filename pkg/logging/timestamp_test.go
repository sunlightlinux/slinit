package logging

import (
	"strings"
	"testing"
	"time"
)

func TestParseTimestampFormat(t *testing.T) {
	tests := []struct {
		in   string
		want TimestampFormat
		err  bool
	}{
		{"", TimestampWallclock, false},
		{"wallclock", TimestampWallclock, false},
		{"time", TimestampWallclock, false},
		{"default", TimestampWallclock, false},
		{"iso", TimestampISO8601, false},
		{"iso8601", TimestampISO8601, false},
		{"tai64n", TimestampTAI64N, false},
		{"tai", TimestampTAI64N, false},
		{"none", TimestampNone, false},
		{"off", TimestampNone, false},
		{"garbage", TimestampWallclock, true},
		{"TAI64N", TimestampWallclock, true}, // case-sensitive
	}
	for _, tc := range tests {
		got, err := ParseTimestampFormat(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseTimestampFormat(%q) err=nil, want err", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTimestampFormat(%q) unexpected err: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseTimestampFormat(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFormatTimestampWallclock(t *testing.T) {
	orig := timestampFormat
	t.Cleanup(func() { timestampFormat = orig })

	SetTimestampFormat(TimestampWallclock)
	ts := formatTimestamp(time.Date(2026, 4, 17, 10, 31, 4, 123_000_000, time.UTC))
	if ts != "10:31:04" {
		t.Errorf("got %q, want 10:31:04", ts)
	}
}

func TestFormatTimestampISO(t *testing.T) {
	orig := timestampFormat
	t.Cleanup(func() { timestampFormat = orig })

	SetTimestampFormat(TimestampISO8601)
	ts := formatTimestamp(time.Date(2026, 4, 17, 10, 31, 4, 213_000_000, time.UTC))
	if ts != "2026-04-17T10:31:04.213" {
		t.Errorf("got %q, want 2026-04-17T10:31:04.213", ts)
	}
}

func TestFormatTimestampTAI64N(t *testing.T) {
	orig := timestampFormat
	t.Cleanup(func() { timestampFormat = orig })

	SetTimestampFormat(TimestampTAI64N)
	// Unix epoch plus nothing → TAI = 2^62 + 0 + 10 = 4611686018427387914
	// Hex: 400000000000000A. Nanoseconds 0 → 00000000.
	ts := formatTimestamp(time.Unix(0, 0))
	if ts != "@400000000000000a00000000" {
		t.Errorf("epoch → %q, want @400000000000000a00000000", ts)
	}

	// Must start with '@', be 25 chars long (@ + 16 hex + 8 hex).
	now := formatTimestamp(time.Now())
	if !strings.HasPrefix(now, "@") {
		t.Errorf("TAI64N missing @ prefix: %q", now)
	}
	if len(now) != 25 {
		t.Errorf("TAI64N length = %d, want 25 (%q)", len(now), now)
	}

	// Nanosecond portion should reflect the sub-second value.
	ts = formatTimestamp(time.Unix(0, 0x1234_5678))
	if !strings.HasSuffix(ts, "12345678") {
		t.Errorf("TAI64N nsecs suffix wrong: %q", ts)
	}
}

func TestFormatTimestampNone(t *testing.T) {
	orig := timestampFormat
	t.Cleanup(func() { timestampFormat = orig })

	SetTimestampFormat(TimestampNone)
	if ts := formatTimestamp(time.Now()); ts != "" {
		t.Errorf("TimestampNone should yield empty string, got %q", ts)
	}
}
