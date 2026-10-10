package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

type followPidLogger struct{}

func (l *followPidLogger) ServiceStarted(name string)               {}
func (l *followPidLogger) ServiceStopped(name string)               {}
func (l *followPidLogger) ServiceFailed(name string, dep bool)      {}
func (l *followPidLogger) Error(format string, args ...interface{}) {}
func (l *followPidLogger) Info(format string, args ...interface{})  {}

func writeFollowSvc(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func loadFollowSvc(t *testing.T, body string) (service.Service, error) {
	t.Helper()
	dir := t.TempDir()
	ss := service.NewServiceSet(&followPidLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)
	writeFollowSvc(t, dir, "svc", body)
	return loader.LoadService("svc")
}

func TestParseFollowPID(t *testing.T) {
	desc, err := Parse(strings.NewReader(
		"type = bgprocess\nfollow-pid = /run/stranger.pid\n"), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.FollowPID != "/run/stranger.pid" {
		t.Errorf("FollowPID = %q", desc.FollowPID)
	}
}

// follow-pid reaches the service, which is what makes BringUp adopt
// instead of launch.
func TestFollowPIDReachesTheService(t *testing.T) {
	svc, err := loadFollowSvc(t, "type = bgprocess\nfollow-pid = /run/stranger.pid\n")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	bg, ok := svc.(*service.BGProcessService)
	if !ok {
		t.Fatalf("expected a BGProcessService, got %T", svc)
	}
	if got := bg.GetFollowPID(); got != "/run/stranger.pid" {
		t.Errorf("GetFollowPID() = %q", got)
	}
}

// A bgprocess normally refuses to load without a command. Following one
// must not, because there is nothing for slinit to run.
func TestFollowPIDLoadsWithoutCommand(t *testing.T) {
	if _, err := loadFollowSvc(t, "type = bgprocess\nfollow-pid = /run/x.pid\n"); err != nil {
		t.Fatalf("follow-pid without command should load: %v", err)
	}
}

func TestFollowPIDRejectsCommand(t *testing.T) {
	_, err := loadFollowSvc(t,
		"type = bgprocess\nfollow-pid = /run/x.pid\ncommand = /usr/bin/daemon\n")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected a mutually-exclusive error, got %v", err)
	}
}

func TestFollowPIDRejectsPidFile(t *testing.T) {
	_, err := loadFollowSvc(t,
		"type = bgprocess\nfollow-pid = /run/x.pid\npid-file = /run/y.pid\n")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected a mutually-exclusive error, got %v", err)
	}
}

// The adopt path lives in BGProcessService, so the directive has no
// meaning on any other type and must say so rather than be ignored.
func TestFollowPIDRejectedOnOtherTypes(t *testing.T) {
	for _, typ := range []string{"process", "scripted", "internal"} {
		body := "type = " + typ + "\ncommand = /bin/true\nfollow-pid = /run/x.pid\n"
		_, err := loadFollowSvc(t, body)
		if err == nil || !strings.Contains(err.Error(), "only meaningful for type = bgprocess") {
			t.Errorf("type = %s: expected a type error, got %v", typ, err)
		}
	}
}
