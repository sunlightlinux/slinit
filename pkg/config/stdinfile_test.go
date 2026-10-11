package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

type stdinFileLogger struct{}

func (l *stdinFileLogger) ServiceStarted(name string)               {}
func (l *stdinFileLogger) ServiceStopped(name string)               {}
func (l *stdinFileLogger) ServiceFailed(name string, dep bool)      {}
func (l *stdinFileLogger) Error(format string, args ...interface{}) {}
func (l *stdinFileLogger) Info(format string, args ...interface{})  {}

func TestParseStandardInputFile(t *testing.T) {
	desc, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/cat\nstandard-input-file = /etc/motd\n"),
		"svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.StandardInputFile != "/etc/motd" {
		t.Errorf("StandardInputFile = %q", desc.StandardInputFile)
	}
}

func loadStdinSvc(t *testing.T, body string) error {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "svc"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := service.NewServiceSet(&stdinFileLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)
	_, err := loader.LoadService("svc")
	return err
}

// Three ways to fill fd 0, one descriptor. Refused rather than resolved
// by precedence: a silent winner is how an operator debugs the input
// they did not get.
func TestStandardInputFileConflictsWithText(t *testing.T) {
	err := loadStdinSvc(t, "type = process\ncommand = /bin/cat\n"+
		"standard-input-file = /etc/motd\nstandard-input-text = hello\n")
	if err == nil || !strings.Contains(err.Error(), "both fill") {
		t.Fatalf("expected a conflict error, got %v", err)
	}
}

func TestStandardInputFileConflictsWithConsumerOf(t *testing.T) {
	err := loadStdinSvc(t, "type = process\ncommand = /bin/cat\n"+
		"standard-input-file = /etc/motd\nconsumer-of: producer\n")
	if err == nil || !strings.Contains(err.Error(), "consumer-of") {
		t.Fatalf("expected a consumer-of conflict, got %v", err)
	}
}

func TestStandardInputFileAloneLoads(t *testing.T) {
	if err := loadStdinSvc(t, "type = process\ncommand = /bin/cat\n"+
		"standard-input-file = /etc/motd\n"); err != nil {
		t.Fatalf("should load: %v", err)
	}
}
