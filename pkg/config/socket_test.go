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

// socket-reuseport is what lets several services hold one host:port, so a
// typo in the directive name must not pass silently as "off".
func TestParseSocketReusePort(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"type = process\ncommand = /bin/true\nsocket-listen = tcp:0.0.0.0:8080\nsocket-reuseport = yes\n", true},
		{"type = process\ncommand = /bin/true\nsocket-listen = tcp:0.0.0.0:8080\nsocket-reuseport = no\n", false},
		{"type = process\ncommand = /bin/true\nsocket-listen = tcp:0.0.0.0:8080\n", false},
	} {
		desc, err := Parse(strings.NewReader(tc.body), "svc", "svc")
		if err != nil {
			t.Fatalf("parse %q: %v", tc.body, err)
		}
		if desc.SocketReusePort != tc.want {
			t.Errorf("SocketReusePort = %v, want %v for:\n%s",
				desc.SocketReusePort, tc.want, tc.body)
		}
	}

	if _, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nsocket-reuseport = maybe\n"), "svc", "svc"); err == nil {
		t.Error("socket-reuseport = maybe should be rejected, not read as false")
	}
}
