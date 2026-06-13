package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseCronCalendar(t *testing.T) {
	input := `
type = process
command = /bin/true
cron-command = /usr/local/bin/backup
cron-calendar = daily
cron-randomized-delay = 30m
cron-persistent = yes
cron-on-error = stop
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.CronCalendar == nil {
		t.Fatal("CronCalendar nil")
	}
	if desc.CronRandomizedDelay != 30*time.Minute {
		t.Errorf("RandomizedDelay: got %v", desc.CronRandomizedDelay)
	}
	if !desc.CronPersistent {
		t.Error("Persistent should be true")
	}
	if desc.CronOnError != "stop" {
		t.Errorf("OnError: got %q want stop", desc.CronOnError)
	}
}

func TestParseCronCalendarRejectsMalformed(t *testing.T) {
	input := "type = process\ncommand = /bin/true\ncron-command = /bin/x\ncron-calendar = not-a-calendar\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Error("expected error for malformed calendar")
	}
}

func TestParseCronRandomizedDelayRejectsNegative(t *testing.T) {
	input := "type = process\ncommand = /bin/true\ncron-command = /bin/x\ncron-calendar = daily\ncron-randomized-delay = -5s\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Error("expected error for negative randomized delay")
	}
}
