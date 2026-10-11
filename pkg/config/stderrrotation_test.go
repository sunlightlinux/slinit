package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

type stderrRotLogger struct{}

func (l *stderrRotLogger) ServiceStarted(name string)               {}
func (l *stderrRotLogger) ServiceStopped(name string)               {}
func (l *stderrRotLogger) ServiceFailed(name string, dep bool)      {}
func (l *stderrRotLogger) Error(format string, args ...interface{}) {}
func (l *stderrRotLogger) Info(format string, args ...interface{})  {}

func TestParseStderrRotationOverrides(t *testing.T) {
	desc, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nlog-type = file\n"+
			"logfile = /tmp/out.log\nlogfile-max-size = 10485760\n"+
			"stderr-logfile = /tmp/err.log\nstderr-logfile-max-size = 1048576\n"+
			"stderr-logfile-max-files = 3\nstderr-logfile-rotate-time = 3600\n"),
		"svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.LogMaxSize != 10485760 {
		t.Errorf("logfile-max-size = %d", desc.LogMaxSize)
	}
	if desc.StderrMaxSize != 1048576 {
		t.Errorf("stderr-logfile-max-size = %d", desc.StderrMaxSize)
	}
	if desc.StderrMaxFiles != 3 {
		t.Errorf("stderr-logfile-max-files = %d", desc.StderrMaxFiles)
	}
	if desc.StderrRotateTime != time.Hour {
		t.Errorf("stderr-logfile-rotate-time = %v", desc.StderrRotateTime)
	}
}

// Unset means inherit, which is what stderr-logfile did on its own
// before these directives existed.
func TestParseStderrRotationDefaultsToZero(t *testing.T) {
	desc, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nlog-type = file\n"+
			"logfile = /tmp/out.log\nstderr-logfile = /tmp/err.log\n"),
		"svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.StderrMaxSize != 0 || desc.StderrMaxFiles != 0 || desc.StderrRotateTime != 0 {
		t.Errorf("expected zero overrides, got %d/%d/%v",
			desc.StderrMaxSize, desc.StderrMaxFiles, desc.StderrRotateTime)
	}
}

func loadStderrRotSvc(t *testing.T, body string) error {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "svc"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := service.NewServiceSet(&stderrRotLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)
	_, err := loader.LoadService("svc")
	return err
}

// An override with no stderr file would be parsed, stored and never
// read. Refused rather than silently ignored.
func TestStderrRotationWithoutStderrFileRejected(t *testing.T) {
	err := loadStderrRotSvc(t, "type = process\ncommand = /bin/true\n"+
		"log-type = file\nlogfile = /tmp/out.log\nstderr-logfile-max-size = 1048576\n")
	if err == nil || !strings.Contains(err.Error(), "no effect without stderr-logfile") {
		t.Fatalf("expected a no-effect error, got %v", err)
	}
}

func TestStderrRotationWithStderrFileLoads(t *testing.T) {
	err := loadStderrRotSvc(t, "type = process\ncommand = /bin/true\n"+
		"log-type = file\nlogfile = /tmp/out.log\nstderr-logfile = /tmp/err.log\n"+
		"stderr-logfile-max-size = 1048576\n")
	if err != nil {
		t.Fatalf("should load: %v", err)
	}
}
