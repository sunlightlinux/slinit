package journal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// SyslogPath is where syslog(3), logger(1) and every other syslog
// client send their messages: a SOCK_DGRAM Unix socket.
const SyslogPath = "/dev/log"

// ErrSyslogActive is returned by NewSyslogReader when something already
// answers on the socket path: another syslog daemon owns it, and taking
// the path over would cut it off from its clients.
var ErrSyslogActive = errors.New("journal: syslog socket is already served by another daemon")

// Rate limit for syslog messages, per sending UID, over a fixed window.
// The ring the journal keeps in PID 1 holds 4096 events, and /dev/log is
// writable by every local user: without a limit one of them could push
// every service's log out of the ring in a fraction of a second. Root
// gets more room because the system's own daemons (sudo, login, dhcpcd)
// log as root, and a burst from them at boot is normal.
const (
	syslogRateWindow    = 10 * time.Second
	syslogRateRoot      = 1000
	syslogRateNonRoot   = 200
	syslogMaxRateTracks = 256 // distinct UIDs tracked per window
)

// syslogMaxDatagram bounds one message. The kernel truncates a longer
// datagram to the buffer; the message is kept, cut short.
const syslogMaxDatagram = 64 * 1024

// SyslogReader serves the syslog socket and emits each message into the
// journal with Transport=syslog, so `slinit-journalctl -t sudo` finds
// what sudo, login, dhcpcd and the like log through syslog(3). Without
// it a system with no syslog daemon drops those messages: the socket
// does not exist, and syslog(3) discards silently.
//
// The sender's identity (_pid, _uid, _gid, _comm, _exe) comes from the
// kernel, through SO_PASSCRED, never from the message: the identifier
// and pid in "sudo[123]:" are whatever the sender wrote, and any local
// user can write "sudo".
type SyslogReader struct {
	emitter *Emitter
	conn    *net.UnixConn
	self    int // our own pid: messages from it are dropped (see handle)

	stopOnce sync.Once
	done     chan struct{}

	// Rate limiting, touched only by the read loop.
	windowStart time.Time
	counts      map[uint32]int
	suppressed  map[uint32]int
	now         func() time.Time
}

// NewSyslogReader binds path as the syslog socket. A socket file nobody
// answers on is stale (left by a previous boot or a slinit that re-
// executed) and is replaced. One something answers on is left alone and
// ErrSyslogActive returned.
func NewSyslogReader(emitter *Emitter, path string) (*SyslogReader, error) {
	if emitter == nil {
		return nil, errors.New("journal: syslog reader needs emitter")
	}
	if path == "" {
		path = SyslogPath
	}
	if c, err := net.DialTimeout("unixgram", path, time.Second); err == nil {
		c.Close()
		return nil, ErrSyslogActive
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("journal: remove stale %s: %w", path, err)
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, fmt.Errorf("journal: bind %s: %w", path, err)
	}
	// Closing a datagram socket leaves its file, and Stop does not remove
	// it: by then another daemon may own the path, and the file lives on
	// /dev, which the next boot starts afresh.
	fail := func(err error) (*SyslogReader, error) {
		conn.Close()
		os.Remove(path)
		return nil, err
	}
	// Every local user may log, as with any syslog daemon.
	if err := os.Chmod(path, 0666); err != nil {
		return fail(fmt.Errorf("journal: chmod %s: %w", path, err))
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return fail(fmt.Errorf("journal: %s: %w", path, err))
	}
	var setErr error
	if err := raw.Control(func(fd uintptr) {
		setErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	}); err != nil {
		return fail(fmt.Errorf("journal: %s: %w", path, err))
	}
	if setErr != nil {
		return fail(fmt.Errorf("journal: SO_PASSCRED on %s: %w", path, setErr))
	}
	return &SyslogReader{
		emitter:    emitter,
		conn:       conn,
		self:       os.Getpid(),
		done:       make(chan struct{}),
		counts:     make(map[uint32]int),
		suppressed: make(map[uint32]int),
		now:        time.Now,
	}, nil
}

// Run reads messages until Stop is called or ctx is done.
func (r *SyslogReader) Run(ctx context.Context) {
	defer close(r.done)
	go func() {
		select {
		case <-ctx.Done():
			r.conn.Close()
		case <-r.done:
		}
	}()

	buf := make([]byte, syslogMaxDatagram)
	oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	for {
		n, oobn, _, _, err := r.conn.ReadMsgUnix(buf, oob)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// A datagram socket has no per-peer failure to recover
			// from; anything else is the socket itself going away.
			fmt.Fprintf(os.Stderr, "journal: syslog read: %v\n", err)
			return
		}
		r.handle(buf[:n], oob[:oobn])
	}
}

// Stop closes the socket and waits for Run to return. Idempotent.
func (r *SyslogReader) Stop() {
	r.stopOnce.Do(func() { r.conn.Close() })
	<-r.done
}

// handle turns one datagram into an event. It runs in PID 1 on bytes any
// local user can send, so a panic here would take the machine down: the
// parser is fuzzed (FuzzParseSyslog), and this recover is the second
// line, dropping the message rather than the system.
func (r *SyslogReader) handle(data, oob []byte) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(os.Stderr, "journal: syslog message dropped, parser panic: %v\n", p)
		}
	}()

	cred, ok := parseCred(oob)
	if !ok {
		return // SO_PASSCRED is set, so the kernel always attaches one
	}
	// slinit's own main log goes to syslog in system mode. It is
	// already in the journal; reading it back would duplicate it.
	if int(cred.Pid) == r.self {
		return
	}
	if !r.allow(cred.Uid) {
		return
	}

	evt := ParseSyslog(data)
	if evt == nil {
		return
	}
	evt.Transport = TransportSyslog
	evt.Pid, evt.Uid, evt.Gid = int(cred.Pid), int(cred.Uid), int(cred.Gid)
	evt.Comm, evt.Exe = procIdentity(int(cred.Pid))
	_ = r.emitter.Emit(evt)
}

// allow applies the per-UID rate limit. The first message after a
// window in which some were suppressed emits one event saying how many,
// so the gap in the log is visible instead of silent.
func (r *SyslogReader) allow(uid uint32) bool {
	now := r.now()
	if now.Sub(r.windowStart) >= syslogRateWindow {
		for u, n := range r.suppressed {
			_ = r.emitter.Emit(&Event{
				Msg: fmt.Sprintf("syslog: suppressed %d messages from uid %d in %s (limit %d)",
					n, u, syslogRateWindow, rateLimitFor(u)),
				Prio:             PriorityWarning,
				SyslogIdentifier: "slinit",
			})
		}
		r.windowStart = now
		clear(r.counts)
		clear(r.suppressed)
	}
	if _, seen := r.counts[uid]; !seen && len(r.counts) >= syslogMaxRateTracks {
		// More distinct senders than the table holds in one window;
		// treat the overflow as one more suppression.
		r.suppressed[uid]++
		return false
	}
	r.counts[uid]++
	if r.counts[uid] > rateLimitFor(uid) {
		r.suppressed[uid]++
		return false
	}
	return true
}

func rateLimitFor(uid uint32) int {
	if uid == 0 {
		return syslogRateRoot
	}
	return syslogRateNonRoot
}

func parseCred(oob []byte) (*unix.Ucred, bool) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, false
	}
	for i := range msgs {
		if cred, err := unix.ParseUnixCredentials(&msgs[i]); err == nil {
			return cred, true
		}
	}
	return nil, false
}

// procIdentity reads /proc/<pid>/comm and exe. A sender that has exited
// already leaves them empty: the kernel's pid is still recorded, and an
// empty field is better than a guessed one.
func procIdentity(pid int) (comm, exe string) {
	if pid <= 0 {
		return "", ""
	}
	p := "/proc/" + strconv.Itoa(pid)
	if b, err := os.ReadFile(p + "/comm"); err == nil {
		comm = strings.TrimRight(string(b), "\n")
	}
	exe, _ = os.Readlink(p + "/exe")
	return comm, exe
}

// ParseSyslog parses one syslog datagram:
//
//	<PRI>Mmm dd hh:mm:ss IDENT[PID]: MESSAGE
//
// which is what glibc and musl syslog(3) and util-linux logger(1) send
// to a local socket. Each part is optional, in the order systemd-
// journald accepts them: a missing <PRI> means user.info, a missing or
// malformed timestamp is left in the message, and without an "IDENT:" or
// "IDENT[PID]:" word there is no identifier. The timestamp is discarded:
// it is the sender's clock, and the event is stamped on arrival.
//
// Returns nil for a message that is empty once whitespace is trimmed.
func ParseSyslog(data []byte) *Event {
	s := strings.TrimRight(string(data), " \t\r\n\x00")
	s = strings.TrimLeft(s, " \t\r\n")

	prio := PriorityInfo
	if p, rest, ok := parseSyslogPRI(s); ok {
		prio, s = p, rest
	}
	s = skipSyslogTimestamp(s)
	ident, s := parseSyslogIdent(s)
	if s == "" {
		return nil
	}
	if len(s) > MaxMessageLen {
		s = s[:MaxMessageLen]
	}
	return &Event{Msg: s, Prio: prio, SyslogIdentifier: ident}
}

// parseSyslogPRI reads "<N>" with N of one to three digits, facility and
// severity together, and returns the severity. Anything else is not a
// priority prefix and is left in place.
func parseSyslogPRI(s string) (Priority, string, bool) {
	if len(s) < 3 || s[0] != '<' {
		return 0, s, false
	}
	end := strings.IndexByte(s, '>')
	if end < 2 || end > 4 {
		return 0, s, false
	}
	n := 0
	for _, c := range s[1:end] {
		if c < '0' || c > '9' {
			return 0, s, false
		}
		n = n*10 + int(c-'0')
	}
	return Priority(n & 7), s[end+1:], true
}

// skipSyslogTimestamp drops an RFC 3164 timestamp, "Mmm dd hh:mm:ss "
// with the day space-padded, when the text starts with exactly that
// shape. A partial match is not a timestamp and stays in the message.
func skipSyslogTimestamp(s string) string {
	const shape = "LLL _N _N:_N:_N " // L letter, N digit, _ digit or space
	if len(s) < len(shape) {
		return s
	}
	for i := 0; i < len(shape); i++ {
		c := s[i]
		switch shape[i] {
		case 'L':
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
				return s
			}
		case 'N':
			if c < '0' || c > '9' {
				return s
			}
		case '_':
			if c != ' ' && (c < '0' || c > '9') {
				return s
			}
		default:
			if c != shape[i] {
				return s
			}
		}
	}
	return s[len(shape):]
}

// parseSyslogIdent splits off a leading "IDENT:" or "IDENT[PID]:" word.
// The word runs to the first whitespace and must end in ':'; otherwise
// the message has no identifier and is returned whole. The bracketed pid
// is the sender's claim and is not kept: the kernel's is. One space after
// the colon is the separator and is dropped.
func parseSyslogIdent(s string) (string, string) {
	end := strings.IndexAny(s, " \t\n")
	if end < 0 {
		end = len(s)
	}
	word := s[:end]
	if len(word) < 2 || word[len(word)-1] != ':' {
		return "", s
	}
	ident := word[:len(word)-1]
	if strings.HasSuffix(ident, "]") {
		if open := strings.LastIndexByte(ident, '['); open >= 0 {
			ident = ident[:open]
		}
	}
	rest := s[end:]
	if rest != "" {
		rest = rest[1:]
	}
	// An identifier longer than a file name is more likely a message
	// that happens to contain a colon than a program's name.
	if ident == "" || len(ident) > 255 {
		return "", rest
	}
	return ident, rest
}
