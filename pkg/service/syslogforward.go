package service

import (
	"fmt"
	"net"
	"os"
	"time"
)

// SyslogForwarder ships log lines to a remote syslog receiver via UDP.
// One connection is held for the lifetime of the forwarder; UDP is
// connectionless from the kernel's perspective so this is effectively
// just a cached destination address that Write() aims at.
//
// The forwarder is deliberately non-blocking on send failures: a
// downed collector must not backpressure the local log path. Failures
// are counted and reported at most every ForwardErrorReportInterval,
// so an operator watching the daemon log gets a signal without a
// flood of the same message for every dropped line.
type SyslogForwarder struct {
	conn       *net.UDPConn
	format     string // "rfc3164" or "rfc5424"
	facility   int    // 0..23
	tag        string // program name / app-name
	hostname   string // cached local hostname (looked up once)
	pid        int    // cached PID
	dest       string // "host:port" — for the error report line
	lastErrLog time.Time
	errCount   uint64
	logger     interface {
		Info(string, ...interface{})
		Error(string, ...interface{})
	}
	serviceName string
}

// ForwardErrorReportInterval bounds the "N send errors since last
// report" heartbeat rate. Fits inside a typical monitoring scrape
// window without spamming a broken remote.
const ForwardErrorReportInterval = 30 * time.Second

// NewSyslogForwarder resolves dest, dials the UDP socket, and caches
// the hostname/PID we need for RFC 3164/5424 framing. Returns an
// error if dest doesn't resolve or the socket can't be created — the
// caller should surface those at load time, before the service is
// allowed to declare itself configured.
func NewSyslogForwarder(dest, format string, facility int, tag, serviceName string,
	logger interface {
		Info(string, ...interface{})
		Error(string, ...interface{})
	}) (*SyslogForwarder, error) {

	udpAddr, err := net.ResolveUDPAddr("udp", dest)
	if err != nil {
		return nil, fmt.Errorf("syslog-forward: resolve %q: %w", dest, err)
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, fmt.Errorf("syslog-forward: dial %q: %w", dest, err)
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}
	if tag == "" {
		tag = serviceName
	}
	if format == "" {
		format = "rfc3164"
	}
	return &SyslogForwarder{
		conn:        conn,
		format:      format,
		facility:    facility,
		tag:         tag,
		hostname:    host,
		pid:         os.Getpid(),
		dest:        dest,
		serviceName: serviceName,
		logger:      logger,
	}, nil
}

// Send emits one log line as a syslog packet. The line is expected
// to have its trailing '\n' still attached; the framing helper
// strips it before embedding the message. Severity is derived from a
// leading "<N>" prefix when present (existing extractSyslogLevel
// path) so services that already speak partial syslog get their PRI
// preserved end-to-end. Otherwise defaults to "info" (6).
func (f *SyslogForwarder) Send(line []byte) {
	if f == nil || f.conn == nil {
		return
	}
	content := line
	if len(content) > 0 && content[len(content)-1] == '\n' {
		content = content[:len(content)-1]
	}
	sev := extractSyslogLevel(content)
	pri := f.facility*8 + sev
	now := time.Now()

	var pkt []byte
	if f.format == "rfc5424" {
		pkt = f.frameRFC5424(pri, now, content)
	} else {
		pkt = f.frameRFC3164(pri, now, content)
	}

	if _, err := f.conn.Write(pkt); err != nil {
		f.errCount++
		if time.Since(f.lastErrLog) >= ForwardErrorReportInterval {
			f.lastErrLog = time.Now()
			if f.logger != nil {
				f.logger.Error("Service '%s': syslog-forward to %s failing (%d errors since last report): %v",
					f.serviceName, f.dest, f.errCount, err)
			}
			f.errCount = 0
		}
	}
}

// frameRFC3164 builds a classic BSD syslog packet:
//
//	<PRI>Mmm dd hh:mm:ss HOSTNAME TAG[PID]: MESSAGE
//
// where the timestamp is local time (no year, no timezone) per the
// spec. Space-efficient; the format that every syslog receiver
// speaks natively.
func (f *SyslogForwarder) frameRFC3164(pri int, ts time.Time, msg []byte) []byte {
	// "Jan  2 15:04:05" — RFC 3164 requires a space in front of a
	// single-digit day, which Go's " 2" verb gives us for free.
	stamp := ts.Format("Jan _2 15:04:05")
	header := fmt.Sprintf("<%d>%s %s %s[%d]: ", pri, stamp, f.hostname, f.tag, f.pid)
	out := make([]byte, 0, len(header)+len(msg))
	out = append(out, header...)
	out = append(out, msg...)
	return out
}

// frameRFC5424 builds a modern structured syslog packet:
//
//	<PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID SD-ELEMENT MESSAGE
//
// Uses "-" for MSGID and structured data (we don't have anything
// meaningful to put there yet). The timestamp is a strict
// RFC 3339-with-microseconds string in UTC.
func (f *SyslogForwarder) frameRFC5424(pri int, ts time.Time, msg []byte) []byte {
	stamp := ts.UTC().Format("2006-01-02T15:04:05.000000Z")
	header := fmt.Sprintf("<%d>1 %s %s %s %d - - ", pri, stamp, f.hostname, f.tag, f.pid)
	out := make([]byte, 0, len(header)+len(msg))
	out = append(out, header...)
	out = append(out, msg...)
	return out
}

// Close releases the UDP socket. Safe to call multiple times.
func (f *SyslogForwarder) Close() {
	if f == nil || f.conn == nil {
		return
	}
	_ = f.conn.Close()
	f.conn = nil
}
