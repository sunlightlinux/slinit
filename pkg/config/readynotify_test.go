package config

import (
	"strings"
	"testing"
)

func TestReadyNotificationParsingPipefd(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
ready-notification = pipefd:3
`
	desc, err := Parse(strings.NewReader(input), "notify-svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.ReadyNotifyFD != 3 {
		t.Errorf("expected ReadyNotifyFD=3, got %d", desc.ReadyNotifyFD)
	}
	if desc.ReadyNotifyVar != "" {
		t.Errorf("expected empty ReadyNotifyVar, got '%s'", desc.ReadyNotifyVar)
	}
}

func TestReadyNotificationParsingPipevar(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
ready-notification = pipevar:NOTIFY_FD
`
	desc, err := Parse(strings.NewReader(input), "notify-svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.ReadyNotifyFD != -1 {
		t.Errorf("expected ReadyNotifyFD=-1, got %d", desc.ReadyNotifyFD)
	}
	if desc.ReadyNotifyVar != "NOTIFY_FD" {
		t.Errorf("expected ReadyNotifyVar='NOTIFY_FD', got '%s'", desc.ReadyNotifyVar)
	}
}

func TestReadyNotificationParsingInvalid(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
ready-notification = invalid
`
	_, err := Parse(strings.NewReader(input), "notify-svc", "test-file")
	if err == nil {
		t.Fatal("expected error for invalid ready-notification value")
	}
	if !strings.Contains(err.Error(), "unrecognised") {
		t.Errorf("expected 'unrecognised' in error, got: %v", err)
	}
}

func TestReadyNotificationParsingPipefdHighFD(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
ready-notification = pipefd:7
`
	desc, err := Parse(strings.NewReader(input), "notify-svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.ReadyNotifyFD != 7 {
		t.Errorf("expected ReadyNotifyFD=7, got %d", desc.ReadyNotifyFD)
	}
}

func TestReadyNotificationParsingEmptyPipevar(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
ready-notification = pipevar:
`
	_, err := Parse(strings.NewReader(input), "notify-svc", "test-file")
	if err == nil {
		t.Fatal("expected error for empty pipevar name")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected 'empty' in error, got: %v", err)
	}
}
