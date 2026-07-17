package config

import (
	"testing"
	"time"
)

// TestParseCronAccuracySec covers the new accuracy directive: happy
// path + negative rejection.
func TestParseCronAccuracySec(t *testing.T) {
	desc, err := parseServiceContent(`
type = process
command = /bin/true
cron-command = /bin/echo tick
cron-calendar = *:0/5
cron-accuracy-sec = 30s
`, "cron-accuracy-probe")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.CronAccuracy != 30*time.Second {
		t.Errorf("cron-accuracy-sec: got %v, want 30s", desc.CronAccuracy)
	}

	if _, err := parseServiceContent(`
type = process
command = /bin/true
cron-command = /bin/echo tick
cron-calendar = *:0/5
cron-accuracy-sec = -3s
`, ""); err == nil {
		t.Error("negative cron-accuracy-sec must be rejected")
	}
}

// TestParseCronOnActiveAlias confirms the systemd-portability alias
// lands in the same field as cron-delay.
func TestParseCronOnActiveAlias(t *testing.T) {
	desc, err := parseServiceContent(`
type = process
command = /bin/true
cron-command = /bin/echo
cron-interval = 60s
cron-on-active = 15s
`, "on-active-alias")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.CronDelay != 15*time.Second {
		t.Errorf("cron-on-active alias: CronDelay=%v, want 15s", desc.CronDelay)
	}
}

// TestParseCronOnUnitActiveAlias — the interval alias.
func TestParseCronOnUnitActiveAlias(t *testing.T) {
	desc, err := parseServiceContent(`
type = process
command = /bin/true
cron-command = /bin/echo
cron-on-unit-active = 45s
`, "on-unit-active-alias")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.CronInterval != 45*time.Second {
		t.Errorf("cron-on-unit-active alias: CronInterval=%v, want 45s", desc.CronInterval)
	}
}
