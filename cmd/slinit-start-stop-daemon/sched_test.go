package main

import "testing"

func TestParseSchedPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
		ok   bool
	}{
		{"other", schedOther, true},
		{"normal", schedOther, true},
		{"fifo", schedFIFO, true},
		{"rr", schedRR, true},
		{"batch", schedBatch, true},
		{"idle", schedIdle, true},
		{"FIFO", schedFIFO, true},
		{"  rr  ", schedRR, true},
		{"", 0, false},
		{"deadline", 0, false},
		{"nope", 0, false},
	}
	for _, tc := range cases {
		got, err := parseSchedPolicy(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("parseSchedPolicy(%q) err=%v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("parseSchedPolicy(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
