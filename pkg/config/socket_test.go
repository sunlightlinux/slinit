package config

import (
	"strings"
	"testing"
)

func TestSocketActivationParsing(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
socket-listen = /tmp/test.sock
socket-permissions = 0660
socket-uid = 1000
socket-gid = 1000
`
	desc, err := Parse(strings.NewReader(input), "sock-svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.SocketPath != "/tmp/test.sock" {
		t.Errorf("expected socket path '/tmp/test.sock', got '%s'", desc.SocketPath)
	}
	if desc.SocketPerms != 0660 {
		t.Errorf("expected socket perms 0660, got %o", desc.SocketPerms)
	}
	if desc.SocketUID != 1000 {
		t.Errorf("expected socket uid 1000, got %d", desc.SocketUID)
	}
	if desc.SocketGID != 1000 {
		t.Errorf("expected socket gid 1000, got %d", desc.SocketGID)
	}
}

func TestSocketParsingDefaultUID(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
socket-listen = /tmp/test.sock
`
	desc, err := Parse(strings.NewReader(input), "sock-svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.SocketUID != -1 {
		t.Errorf("expected default socket uid -1, got %d", desc.SocketUID)
	}
	if desc.SocketGID != -1 {
		t.Errorf("expected default socket gid -1, got %d", desc.SocketGID)
	}
}

func TestSocketParsingInvalidUID(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
socket-listen = /tmp/test.sock
socket-uid = notanumber
`
	_, err := Parse(strings.NewReader(input), "sock-svc", "test-file")
	if err == nil {
		t.Fatal("expected error for invalid socket-uid")
	}
	if !strings.Contains(err.Error(), "invalid socket uid") {
		t.Errorf("expected 'invalid socket uid' in error, got: %v", err)
	}
}
