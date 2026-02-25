package service

import (
	"testing"
)

func TestLogfileSetters(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "app")
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails("/var/log/app.log", 0640, 1000, 100)

	if svc.GetLogType() != LogToFile {
		t.Errorf("expected LogToFile, got %v", svc.GetLogType())
	}
	if svc.GetLogFile() != "/var/log/app.log" {
		t.Errorf("expected logfile '/var/log/app.log', got '%s'", svc.GetLogFile())
	}
}

func TestLogfileSettersBGProcess(t *testing.T) {
	set, _ := newTestSet()

	svc := NewBGProcessService(set, "daemon")
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails("/var/log/daemon.log", 0600, -1, -1)

	if svc.GetLogType() != LogToFile {
		t.Errorf("expected LogToFile, got %v", svc.GetLogType())
	}
	if svc.GetLogFile() != "/var/log/daemon.log" {
		t.Errorf("expected logfile '/var/log/daemon.log', got '%s'", svc.GetLogFile())
	}
}

func TestLogfileSettersScripted(t *testing.T) {
	set, _ := newTestSet()

	svc := NewScriptedService(set, "setup")
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails("/var/log/setup.log", 0644, -1, -1)

	if svc.GetLogType() != LogToFile {
		t.Errorf("expected LogToFile, got %v", svc.GetLogType())
	}
	if svc.GetLogFile() != "/var/log/setup.log" {
		t.Errorf("expected logfile '/var/log/setup.log', got '%s'", svc.GetLogFile())
	}
}
