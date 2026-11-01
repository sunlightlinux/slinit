package journal

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestParseSyslog(t *testing.T) {
	cases := []struct {
		in    string
		prio  Priority
		ident string
		msg   string
	}{
		// What glibc syslog(3) and logger(1) send to a local socket.
		{"<86>Nov  1 16:00:00 sudo[123]: root : TTY=pts/0 ; COMMAND=/bin/ls",
			PriorityInfo, "sudo", "root : TTY=pts/0 ; COMMAND=/bin/ls"},
		{"<38>Nov 11 09:05:07 login: ROOT LOGIN ON ttyS0", PriorityInfo, "login", "ROOT LOGIN ON ttyS0"},
		{"<27>Jan  2 03:04:05 dhcpcd[400]: eth0: no IPv6 Routers available",
			PriorityError, "dhcpcd", "eth0: no IPv6 Routers available"},
		// No timestamp, no priority.
		{"myapp: hello", PriorityInfo, "myapp", "hello"},
		{"<13>tag[9]: x", PriorityNotice, "tag", "x"},
		// No identifier: the first word does not end in ':'.
		{"<14>just a line", PriorityInfo, "", "just a line"},
		{"<14>Nov  1 16:00:00 no ident here", PriorityInfo, "", "no ident here"},
		// A malformed timestamp stays in the message.
		{"<14>Nov 1 16:00:00 x: y", PriorityInfo, "", "Nov 1 16:00:00 x: y"},
		// A malformed priority is not stripped; the word is then the
		// identifier, as systemd-journald reads it.
		{"<abc>x: y", PriorityInfo, "<abc>x", "y"},
		{"<1234> x: y", PriorityInfo, "", "<1234> x: y"},
		// Trailing newline / NUL that some clients send are trimmed.
		{"<14>a: b\n\x00", PriorityInfo, "a", "b"},
		// Only the first space after the colon is the separator.
		{"a:  indented", PriorityInfo, "a", " indented"},
	}
	for _, c := range cases {
		e := ParseSyslog([]byte(c.in))
		if e == nil {
			t.Errorf("ParseSyslog(%q) = nil", c.in)
			continue
		}
		if e.Prio != c.prio || e.SyslogIdentifier != c.ident || e.Msg != c.msg {
			t.Errorf("ParseSyslog(%q) = prio %d ident %q msg %q; want %d %q %q",
				c.in, e.Prio, e.SyslogIdentifier, e.Msg, c.prio, c.ident, c.msg)
		}
	}
	for _, in := range []string{"", "  \n", "<14>", "<14>Nov  1 16:00:00 ", "ident:"} {
		if e := ParseSyslog([]byte(in)); e != nil && e.Msg == "" {
			t.Errorf("ParseSyslog(%q) returned an event with an empty message", in)
		}
	}
}

func FuzzParseSyslog(f *testing.F) {
	for _, s := range []string{
		"<86>Nov  1 16:00:00 sudo[123]: msg", "x:", "<", "<9", "<>", "[]:", "a[: b",
		"<191>Abc 99 99:99:99 ]:", strings.Repeat("a", 300) + ": m",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		e := ParseSyslog(data)
		if e == nil {
			return
		}
		if !e.Prio.Valid() {
			t.Fatalf("priority %d out of range for %q", e.Prio, data)
		}
		if e.Msg == "" || len(e.Msg) > MaxMessageLen {
			t.Fatalf("message length %d for %q", len(e.Msg), data)
		}
		if strings.ContainsAny(e.SyslogIdentifier, " \t\n") {
			t.Fatalf("identifier %q contains whitespace", e.SyslogIdentifier)
		}
	})
}

// newTestSyslog starts a reader on a socket in a temp dir and returns it
// with its buffer. The reader would drop our own messages as its own
// (it runs in PID 1 and skips the pid it is in), so that is turned off.
func newTestSyslog(t *testing.T) (*SyslogReader, *EventBuffer, string) {
	t.Helper()
	InitIDs("testhost")
	buf := NewEventBuffer(64)
	em := NewEmitter(buf, "/dev/null-nope")
	path := filepath.Join(t.TempDir(), "log")
	r, err := NewSyslogReader(em, path)
	if err != nil {
		t.Fatal(err)
	}
	r.self = -1
	go r.Run(context.Background())
	t.Cleanup(r.Stop)
	return r, buf, path
}

func sendSyslog(t *testing.T, path, msg string) {
	t.Helper()
	c, err := net.Dial("unixgram", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
}

func waitEvents(t *testing.T, buf *EventBuffer, n int) []*Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if evs, _ := buf.Snapshot(); len(evs) >= n {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	evs, _ := buf.Snapshot()
	t.Fatalf("got %d events, want %d", len(evs), n)
	return nil
}

// The sender's identity is the kernel's, not the message's. This one
// cannot tell the kernel's pid from Emit stamping its own, since here
// they are the same process; TestSyslogReaderDropsOwnMessages can, with
// a pid of its choosing, and is the test that catches Emit overwriting.
func TestSyslogReaderStampsSenderIdentity(t *testing.T) {
	_, buf, path := newTestSyslog(t)
	sendSyslog(t, path, "<11>Nov  1 16:00:00 sudo[1]: forged pid in the text")

	e := waitEvents(t, buf, 1)[0]
	if e.Transport != TransportSyslog {
		t.Errorf("transport = %q, want syslog", e.Transport)
	}
	if e.SyslogIdentifier != "sudo" || e.Msg != "forged pid in the text" || e.Prio != PriorityError {
		t.Errorf("parsed as ident %q msg %q prio %d", e.SyslogIdentifier, e.Msg, e.Prio)
	}
	if e.Pid != os.Getpid() || e.Uid != os.Getuid() || e.Gid != os.Getgid() {
		t.Errorf("identity = pid %d uid %d gid %d, want the sender's %d %d %d",
			e.Pid, e.Uid, e.Gid, os.Getpid(), os.Getuid(), os.Getgid())
	}
	if e.Comm == "" {
		t.Error("_comm not filled from /proc")
	}
}

// slinit's own main log goes to syslog in system mode; reading it back
// would put every line in the journal twice. Driven through handle with
// hand-built credentials, since a test cannot send from another pid.
func TestSyslogReaderDropsOwnMessages(t *testing.T) {
	InitIDs("testhost")
	buf := NewEventBuffer(8)
	r := &SyslogReader{
		emitter:    NewEmitter(buf, "/dev/null-nope"),
		self:       4242,
		counts:     map[uint32]int{},
		suppressed: map[uint32]int{},
		now:        time.Now,
	}
	cred := func(pid int32) []byte {
		return unix.UnixCredentials(&unix.Ucred{Pid: pid, Uid: 0, Gid: 0})
	}
	r.handle([]byte("slinit: from ourselves"), cred(4242))
	r.handle([]byte("other: from someone else"), cred(4243))
	r.handle([]byte("nocred: no credentials attached"), nil)

	evs, _ := buf.Snapshot()
	if len(evs) != 1 || evs[0].SyslogIdentifier != "other" || evs[0].Pid != 4243 {
		for _, e := range evs {
			t.Logf("event: ident %q pid %d", e.SyslogIdentifier, e.Pid)
		}
		t.Fatalf("got %d events, want only pid 4243's", len(evs))
	}
}

func TestSyslogReaderRateLimit(t *testing.T) {
	InitIDs("testhost")
	buf := NewEventBuffer(4096)
	r := &SyslogReader{
		emitter:    NewEmitter(buf, "/dev/null-nope"),
		counts:     map[uint32]int{},
		suppressed: map[uint32]int{},
	}
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }

	allowed := 0
	for i := 0; i < syslogRateNonRoot+50; i++ {
		if r.allow(1000) {
			allowed++
		}
	}
	if allowed != syslogRateNonRoot {
		t.Errorf("uid 1000 allowed %d in one window, want %d", allowed, syslogRateNonRoot)
	}
	// Root has its own, larger budget in the same window.
	if !r.allow(0) {
		t.Error("root refused although only uid 1000 was over its limit")
	}

	// The next window starts with a note of what was dropped.
	now = now.Add(syslogRateWindow)
	if !r.allow(1000) {
		t.Error("uid 1000 still refused in a new window")
	}
	evs, _ := buf.Snapshot()
	if len(evs) != 1 || !strings.Contains(evs[0].Msg, "suppressed 50 messages from uid 1000") {
		t.Fatalf("summary events = %v", evs)
	}
}

// Another syslog daemon already on the path keeps it.
func TestNewSyslogReaderLeavesActiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	other, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	InitIDs("testhost")
	_, err = NewSyslogReader(NewEmitter(NewEventBuffer(8), "/dev/null-nope"), path)
	if !errors.Is(err, ErrSyslogActive) {
		t.Fatalf("NewSyslogReader beside a live socket = %v, want ErrSyslogActive", err)
	}
	if c, err := net.Dial("unixgram", path); err != nil {
		t.Fatalf("the other daemon's socket no longer answers: %v", err)
	} else {
		c.Close()
	}
}

// A socket file nobody answers on, left by a previous boot or a slinit
// that re-executed, is replaced.
func TestNewSyslogReaderReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	dead, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	dead.Close() // a datagram socket leaves its file behind, as a crash would

	InitIDs("testhost")
	r, err := NewSyslogReader(NewEmitter(NewEventBuffer(8), "/dev/null-nope"), path)
	if err != nil {
		t.Fatalf("NewSyslogReader over a stale socket: %v", err)
	}
	go r.Run(context.Background())
	defer r.Stop()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0666 {
		t.Errorf("socket mode = %v, want 0666 so every user can log", st.Mode().Perm())
	}
}
