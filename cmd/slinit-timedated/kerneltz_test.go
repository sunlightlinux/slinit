package main

import (
	"reflect"
	"testing"
	"time"
)

// The first settimeofday with a timezone after boot shifts the clock by
// that offset and turns the RTC into local time; on sl-demo1 a time zone
// change from GNOME moved the clock two hours back. A zero offset must
// go first so the real one never lands in that slot.
func TestSetKernelTimezoneSealsTheWarpFirst(t *testing.T) {
	var calls []int32
	orig, origLocal := setKernelTZ, time.Local
	setKernelTZ = func(m int32) { calls = append(calls, m) }
	time.Local = time.FixedZone("EET", 2*3600)
	t.Cleanup(func() { setKernelTZ, time.Local = orig, origLocal })

	setKernelTimezone()
	if want := []int32{0, -120}; !reflect.DeepEqual(calls, want) {
		t.Errorf("settimeofday timezones = %v, want %v (zero first)", calls, want)
	}
}
