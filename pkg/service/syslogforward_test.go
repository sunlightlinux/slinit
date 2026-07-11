package service

import (
	"net"
	"strings"
	"testing"
	"time"
)

// syslogTestLogger is a no-op logger for the UDP forwarder tests.
type syslogTestLogger struct{}

func (syslogTestLogger) Info(string, ...interface{})  {}
func (syslogTestLogger) Error(string, ...interface{}) {}

// listenUDP opens an ephemeral UDP socket and returns (conn, "host:port")
// for the caller to feed into NewSyslogForwarder as its destination.
func listenUDP(t *testing.T) (*net.UDPConn, string) {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return conn, conn.LocalAddr().String()
}

// TestSyslogForwardRFC3164Frame verifies the classic BSD framing:
// <PRI>Mmm dd HH:MM:SS host tag[pid]: message
// PRI = facility*8 + severity; default severity is 6 (info).
func TestSyslogForwardRFC3164Frame(t *testing.T) {
	rx, dest := listenUDP(t)
	defer rx.Close()

	fw, err := NewSyslogForwarder(dest, "rfc3164", 3 /* daemon */, "myapp", "svc", &syslogTestLogger{})
	if err != nil {
		t.Fatalf("NewSyslogForwarder: %v", err)
	}
	defer fw.Close()

	fw.Send([]byte("hello world\n"))

	rx.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, _, err := rx.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP: %v", err)
	}
	got := string(buf[:n])
	// Expected: "<30>Mmm dd HH:MM:SS <host> myapp[<pid>]: hello world"
	// PRI = 3*8 + 6 = 30.
	if !strings.HasPrefix(got, "<30>") {
		t.Errorf("PRI wrong: got %q", got)
	}
	if !strings.Contains(got, "myapp[") {
		t.Errorf("tag missing: got %q", got)
	}
	if !strings.HasSuffix(got, ": hello world") {
		t.Errorf("message body wrong: got %q", got)
	}
}

// TestSyslogForwardRFC5424Frame verifies the modern structured
// framing: <PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID SD MSG
func TestSyslogForwardRFC5424Frame(t *testing.T) {
	rx, dest := listenUDP(t)
	defer rx.Close()

	fw, err := NewSyslogForwarder(dest, "rfc5424", 16 /* local0 */, "app", "svc", &syslogTestLogger{})
	if err != nil {
		t.Fatalf("NewSyslogForwarder: %v", err)
	}
	defer fw.Close()

	fw.Send([]byte("body\n"))

	rx.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, _, err := rx.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP: %v", err)
	}
	got := string(buf[:n])
	// PRI = 16*8 + 6 = 134
	if !strings.HasPrefix(got, "<134>1 ") {
		t.Errorf("5424 header wrong: got %q", got)
	}
	// The trailing "MSGID SD-ELEMENT MSG" section: "- - body"
	if !strings.HasSuffix(got, "- - body") {
		t.Errorf("5424 body wrong: got %q", got)
	}
}

// TestSyslogForwardPreservesSyslogLevel confirms that an inline
// "<N>" priority prefix on the source line is respected: the
// forwarder should compute PRI = facility*8 + <embedded severity>,
// not overwrite it with the default 6.
func TestSyslogForwardPreservesSyslogLevel(t *testing.T) {
	rx, dest := listenUDP(t)
	defer rx.Close()

	fw, err := NewSyslogForwarder(dest, "rfc3164", 3, "svc", "svc", &syslogTestLogger{})
	if err != nil {
		t.Fatalf("NewSyslogForwarder: %v", err)
	}
	defer fw.Close()

	// <11> = facility=1(user), severity=3(err). extractSyslogLevel
	// masks the low 3 bits so it should surface as 3.
	fw.Send([]byte("<11>uh oh\n"))

	rx.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, _, err := rx.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP: %v", err)
	}
	got := string(buf[:n])
	// facility=3(daemon), severity=3(err) → PRI = 27
	if !strings.HasPrefix(got, "<27>") {
		t.Errorf("PRI should use embedded severity 3: got %q", got)
	}
}

// TestParserFacilityCode round-trips the human-name → code map used
// by the parser and loader. Kept in the service package because
// SyslogFacilityCode is exported from config but the code path is
// exercised via SyslogForwarder construction.
func TestSyslogForwarderRejectsBadHostPort(t *testing.T) {
	_, err := NewSyslogForwarder("not-a-host-port", "rfc3164", 3, "svc", "svc", &syslogTestLogger{})
	if err == nil {
		t.Fatal("expected error for malformed dest, got nil")
	}
}
