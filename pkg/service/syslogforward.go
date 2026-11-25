package service

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
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
	// conn is nil until a destination named by host has resolved.
	// lookupPending says the lookup is still to be done: the first Send
	// does it, on the goroutine that reads the service's output, and
	// only that goroutine reads or clears it. Close cancels lookupCtx,
	// so a lookup in progress gives up; closeMu orders the store of
	// the dialled conn against Close.
	conn          atomic.Pointer[net.UDPConn]
	lookupPending bool
	resolver      *net.Resolver // syslogForwardResolver when created
	lookupCtx     context.Context
	cancelLookup  context.CancelFunc
	closeMu       sync.Mutex
	closed        bool

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

// syslogForwardResolver resolves a destination given by host name.
// A variable so tests can stand in a resolver that never answers.
var syslogForwardResolver = net.DefaultResolver

// syslogForwardResolveTimeout bounds the lookup of a destination
// given by host name.
const syslogForwardResolveTimeout = 30 * time.Second

// NewSyslogForwarder dials the UDP socket for dest and caches the
// hostname/PID we need for RFC 3164/5424 framing. Returns an error if
// dest is malformed or, for an IP literal, the socket can't be created.
//
// A dest naming a host is not resolved here: this runs from
// startProcess under queueMu, and a resolver that does not answer held
// it for 10 s (glibc's default timeout times attempts). The first Send
// resolves it instead, blocking the goroutine that reads the service's
// output, so the service's pipe holds its lines meanwhile and none is
// lost. A failed lookup is logged and leaves the forwarder disabled,
// as a failed resolve always did.
func NewSyslogForwarder(dest, format string, facility int, tag, serviceName string,
	logger interface {
		Info(string, ...interface{})
		Error(string, ...interface{})
	}) (*SyslogForwarder, error) {

	addrHost, _, err := net.SplitHostPort(dest)
	if err != nil {
		return nil, fmt.Errorf("syslog-forward: resolve %q: %w", dest, err)
	}
	var conn *net.UDPConn
	literal := addrHost == ""
	if !literal {
		_, perr := netip.ParseAddr(addrHost)
		literal = perr == nil
	}
	if literal {
		// No DNS involved: resolve and dial here, as before.
		udpAddr, err := net.ResolveUDPAddr("udp", dest)
		if err != nil {
			return nil, fmt.Errorf("syslog-forward: resolve %q: %w", dest, err)
		}
		conn, err = net.DialUDP("udp", nil, udpAddr)
		if err != nil {
			return nil, fmt.Errorf("syslog-forward: dial %q: %w", dest, err)
		}
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
	f := &SyslogForwarder{
		format:      format,
		facility:    facility,
		tag:         tag,
		hostname:    host,
		pid:         os.Getpid(),
		dest:        dest,
		serviceName: serviceName,
		logger:      logger,
	}
	if conn != nil {
		f.conn.Store(conn)
	} else {
		f.lookupPending = true
		f.resolver = syslogForwardResolver
		f.lookupCtx, f.cancelLookup = context.WithCancel(context.Background())
	}
	return f, nil
}

// resolveAndDial looks up a destination given by host name and dials
// it. Called by the first Send; Close or abortLookup make it give up.
func (f *SyslogForwarder) resolveAndDial(r *net.Resolver) {
	ctx, cancel := context.WithTimeout(f.lookupCtx, syslogForwardResolveTimeout)
	defer cancel()
	c, err := f.dialByName(ctx, r)
	if err != nil {
		if f.logger != nil {
			f.logger.Error("Service '%s': syslog-forward disabled (%v)", f.serviceName, err)
		}
		return
	}
	f.closeMu.Lock()
	defer f.closeMu.Unlock()
	if f.closed {
		_ = c.Close()
		return
	}
	f.conn.Store(c)
}

// dialByName resolves f.dest and dials it. Like net.ResolveUDPAddr, it
// takes the first IPv4 address when there is one: UDP gives no
// connection error to fall back on, so "localhost" must not land on an
// unanswered ::1.
func (f *SyslogForwarder) dialByName(ctx context.Context, r *net.Resolver) (*net.UDPConn, error) {
	host, portName, _ := net.SplitHostPort(f.dest)
	ips, err := r.LookupNetIP(ctx, "ip", host)
	if err == nil && len(ips) == 0 {
		err = fmt.Errorf("no addresses")
	}
	if err != nil {
		return nil, fmt.Errorf("syslog-forward: resolve %q: %w", f.dest, err)
	}
	port, err := r.LookupPort(ctx, "udp", portName)
	if err != nil {
		return nil, fmt.Errorf("syslog-forward: resolve %q: %w", f.dest, err)
	}
	ip := ips[0]
	for _, a := range ips {
		if a.Unmap().Is4() {
			ip = a.Unmap()
			break
		}
	}
	conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port))))
	if err != nil {
		return nil, fmt.Errorf("syslog-forward: dial %q: %w", f.dest, err)
	}
	return conn, nil
}

// Send emits one log line as a syslog packet. The line is expected
// to have its trailing '\n' still attached; the framing helper
// strips it before embedding the message. Severity is derived from a
// leading "<N>" prefix when present (existing extractSyslogLevel
// path) so services that already speak partial syslog get their PRI
// preserved end-to-end. Otherwise defaults to "info" (6).
func (f *SyslogForwarder) Send(line []byte) {
	if f == nil {
		return
	}
	conn := f.conn.Load()
	if conn == nil {
		if !f.lookupPending {
			return
		}
		f.lookupPending = false
		f.resolveAndDial(f.resolver)
		if conn = f.conn.Load(); conn == nil {
			return
		}
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

	if _, err := conn.Write(pkt); err != nil {
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

// abortLookup makes a lookup in progress in Send give up. For a caller
// about to wait, under queueMu, for the goroutine that calls Send.
func (f *SyslogForwarder) abortLookup() {
	if f != nil && f.cancelLookup != nil {
		f.cancelLookup()
	}
}

// Close releases the UDP socket. Safe to call multiple times.
func (f *SyslogForwarder) Close() {
	if f == nil {
		return
	}
	f.abortLookup()
	f.closeMu.Lock()
	f.closed = true
	conn := f.conn.Swap(nil)
	f.closeMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}
