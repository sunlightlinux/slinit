package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestLogfileConfigParsing(t *testing.T) {
	input := `
type = process
command = /bin/app
log-type = file
logfile = /var/log/app.log
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.LogType != service.LogToFile {
		t.Errorf("expected LogToFile, got %v", desc.LogType)
	}
	if desc.LogFile != "/var/log/app.log" {
		t.Errorf("expected logfile '/var/log/app.log', got '%s'", desc.LogFile)
	}
}

func TestLogfileAutoEnable(t *testing.T) {
	input := `
type = process
command = /bin/app
logfile = /var/log/app.log
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// logfile without explicit log-type should auto-set LogToFile
	if desc.LogType != service.LogToFile {
		t.Errorf("expected LogToFile (auto-enabled), got %v", desc.LogType)
	}
}

func TestLogfilePermissions(t *testing.T) {
	input := `
type = process
command = /bin/app
logfile = /var/log/app.log
logfile-permissions = 0640
logfile-uid = 1000
logfile-gid = 100
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.LogFilePerms != 0640 {
		t.Errorf("expected perms 0640, got %o", desc.LogFilePerms)
	}
	if desc.LogFileUID != 1000 {
		t.Errorf("expected uid 1000, got %d", desc.LogFileUID)
	}
	if desc.LogFileGID != 100 {
		t.Errorf("expected gid 100, got %d", desc.LogFileGID)
	}
}

func TestLogfileDefaultPerms(t *testing.T) {
	input := `
type = process
command = /bin/app
logfile = /var/log/app.log
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.LogFilePerms != 0600 {
		t.Errorf("expected default perms 0600, got %o", desc.LogFilePerms)
	}
	if desc.LogFileUID != -1 {
		t.Errorf("expected default uid -1, got %d", desc.LogFileUID)
	}
	if desc.LogFileGID != -1 {
		t.Errorf("expected default gid -1, got %d", desc.LogFileGID)
	}
}

func TestLogfileLoaderSetup(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testLogfileLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	content := "type = process\ncommand = /bin/app\nlogfile = /var/log/app.log\nlogfile-permissions = 0640\n"
	path := filepath.Join(dir, "app")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	svc, err := loader.LoadService("app")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	ps, ok := svc.(*service.ProcessService)
	if !ok {
		t.Fatal("expected ProcessService")
	}
	if ps.GetLogType() != service.LogToFile {
		t.Errorf("expected LogToFile, got %v", ps.GetLogType())
	}
	if ps.GetLogFile() != "/var/log/app.log" {
		t.Errorf("expected logfile '/var/log/app.log', got '%s'", ps.GetLogFile())
	}
}

type testLogfileLogger struct{}

func (l *testLogfileLogger) ServiceStarted(name string)              {}
func (l *testLogfileLogger) ServiceStopped(name string)              {}
func (l *testLogfileLogger) ServiceFailed(name string, dep bool)     {}
func (l *testLogfileLogger) Error(format string, args ...interface{}) {}
func (l *testLogfileLogger) Info(format string, args ...interface{})  {}
