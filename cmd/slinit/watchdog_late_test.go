package main

import "testing"

func TestWatchdogMayAppearLater(t *testing.T) {
	for _, c := range []struct {
		pid1, container bool
		device          string
		want            bool
	}{
		{true, false, "", true},
		{true, false, "/dev/missing", false}, // the documented way to turn it off
		{true, true, "", false},
		{false, true, "", false},
		{false, false, "", false},
	} {
		if got := watchdogMayAppearLater(c.pid1, c.container, c.device); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
