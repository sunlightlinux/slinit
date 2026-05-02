package config

import (
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestWatchdogTimeoutParsing(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
ready-notification = pipefd:3
watchdog-timeout = 30s
`
	desc, err := Parse(strings.NewReader(input), "wd-svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.WatchdogTimeout != 30*time.Second {
		t.Errorf("WatchdogTimeout = %v, want 30s", desc.WatchdogTimeout)
	}
}

func TestWatchdogTimeoutInvalidDuration(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
watchdog-timeout = not-a-duration
`
	if _, err := Parse(strings.NewReader(input), "wd-svc", "test-file"); err == nil {
		t.Fatal("expected parse error for invalid duration")
	}
}

func TestWatchdogTimeoutZeroRejected(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
watchdog-timeout = 0s
`
	_, err := Parse(strings.NewReader(input), "wd-svc", "test-file")
	if err == nil {
		t.Fatal("expected parse error for zero watchdog-timeout")
	}
	if !strings.Contains(err.Error(), "must be > 0") {
		t.Errorf("expected 'must be > 0' in error, got: %v", err)
	}
}

func TestWatchdogTimeoutWithoutReadyNotificationRejected(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "lonely",
		"type = process\ncommand = /bin/true\nwatchdog-timeout = 30s\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("lonely")
	if err == nil {
		t.Fatal("expected load error: watchdog without ready-notification")
	}
	if !strings.Contains(err.Error(), "ready-notification") {
		t.Errorf("expected 'ready-notification' in error, got: %v", err)
	}
}

func TestWatchdogTimeoutOnBgProcessRejected(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "bg",
		"type = bgprocess\ncommand = /bin/true\npid-file = /tmp/x.pid\nwatchdog-timeout = 30s\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	_, err := loader.LoadService("bg")
	if err == nil {
		t.Fatal("expected load error: watchdog on bgprocess type")
	}
}

func TestWatchdogTimeoutWithReadyNotificationLoads(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "guarded",
		"type = process\n"+
			"command = /bin/sleep 60\n"+
			"ready-notification = pipefd:3\n"+
			"watchdog-timeout = 30s\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("guarded")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	ps, ok := svc.(*service.ProcessService)
	if !ok {
		t.Fatalf("expected *service.ProcessService, got %T", svc)
	}
	if !ps.HasWatchdog() {
		t.Error("expected HasWatchdog() = true after load")
	}
	if ps.WatchdogTimeout() != 30*time.Second {
		t.Errorf("WatchdogTimeout() = %v, want 30s", ps.WatchdogTimeout())
	}
}
