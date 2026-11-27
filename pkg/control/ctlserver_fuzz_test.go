package control

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// FuzzControlServer drives a live in-process control server, holding a
// small set of real services (internal, process, scripted, triggered,
// a dependency chain, one with a log buffer), with a fuzzer-chosen
// sequence of packets on two client connections: every opcode the
// server knows plus a few it does not, with payloads that are raw,
// truncated, or built around live, closed and never-allocated handles
// and good and bad service names. Between the packets it can also
// rewrite a service description, so reload and reload-all see changed
// dependencies and types.
//
// The control socket is root-only, but a buggy or old client must not
// be able to take PID 1 down. After each input the harness checks:
//
//   - no panic (a panic in a connection goroutine kills init);
//   - every packet is answered within opDeadline (a handler that blocks,
//     possibly under the graph lock, freezes everything);
//   - a fresh connection is still served, including a command that
//     takes the graph lock;
//   - the service set holds its invariants (state enums in range, a
//     steady target, requiredBy >= 0 and equal to the acquisitions
//     held on it, no STARTED service with a STOPPED hard dependency);
//   - once torn down, no connection or goroutine is left behind.
//
// Nothing outside the test's own children is touched: shutdown,
// switch-root, suspend and wall go to stubs; signals reach only the
// services' own process groups; enable's waits-for.d links land in the
// test's temporary service directory.
func FuzzControlServer(f *testing.F) {
	// A fuzz worker talks to its coordinator over inherited fds that are
	// not close-on-exec, so every service the worker starts inherits
	// them. A worker killed mid-input (minimizing a slow one) then left
	// a SIGSTOPped service holding the pipe, and the coordinator waited
	// on it for good.
	for fd := 3; fd < 16; fd++ {
		syscall.CloseOnExec(fd)
	}
	for _, seed := range ctlFuzzSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		runCtlFuzz(t, data)
	})
}

// TestControlServerRandomOps runs the fuzz body over pseudo-random
// inputs from a fixed seed, so the same coverage runs in `go test`
// without -fuzz. CTLFUZZ_ITERS raises the count for a long soak;
// CTLFUZZ_SEED picks another sequence.
func TestControlServerRandomOps(t *testing.T) {
	iters := 8
	if testing.Short() {
		iters = 2
	}
	if v, err := strconv.Atoi(os.Getenv("CTLFUZZ_ITERS")); err == nil && v > 0 {
		iters = v
	}
	seed := int64(20261126)
	if v, err := strconv.ParseInt(os.Getenv("CTLFUZZ_SEED"), 10, 64); err == nil {
		seed = v
	}
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < iters; i++ {
		data := make([]byte, 64+rng.Intn(448))
		rng.Read(data)
		ok := t.Run(fmt.Sprintf("iter%d", i), func(t *testing.T) {
			runCtlFuzz(t, data)
		})
		if !ok {
			t.Logf("failing input (seed %d, iter %d): %x", seed, i, data)
			return
		}
	}
}

const (
	opDeadline     = 5 * time.Second
	maxCtlFuzzOps  = 48
	goroutineSlack = 4
)

// ctlFuzzCmds is every opcode in protocol.go plus some unassigned ones.
var ctlFuzzCmds = []uint8{
	CmdQueryVersion, CmdFindService, CmdLoadService, CmdStartService,
	CmdStopService, CmdWakeService, CmdReleaseService, CmdUnpinService,
	CmdListServices, CmdUnloadService, CmdShutdown, CmdAddDep, CmdRmDep,
	CmdQueryLoadMech, CmdEnableService, CmdQueryServiceName,
	CmdReloadService, CmdSetEnv, CmdServiceStatus, CmdSetTrigger,
	CmdCatLog, CmdSignal, CmdQueryServiceDscDir, CmdCloseHandle,
	CmdGetAllEnv, CmdListServices5, CmdServiceStatus5, CmdListenEnv,
	CmdServiceStatus6, CmdEnableServiceV7, CmdRmDepV7, CmdBootTime,
	CmdDisableService, CmdQueryDependents, CmdPauseService,
	CmdContinueService, CmdOnceService, CmdQueryDependencies,
	CmdQueryDescription, CmdRunAction, CmdListActions,
	CmdScheduleShutdown, CmdCancelShutdown, CmdQueryShutdown,
	CmdReloadAll, CmdReloadSignal, CmdResetEnv, CmdQueryMetadata,
	CmdActivateProfile, CmdQueryProfile, CmdListProfiles,
	CmdQueryBundleMembers, CmdWallNotice, CmdResetFailed,
	CmdFreezeService, CmdThawService, CmdJournalQuery,
	CmdJournalSubscribe, CmdDisableServiceV7, CmdQueryServiceLoadDir,
	CmdSwitchRoot, CmdSuspend, CmdServiceShow, CmdStartAll,
	CmdServiceRusage,
	CmdCleanService,
	// Unassigned.
	31, 32, 33, 34, 69, 99, 200, 255,
}

// ctlFuzzTimeouts bounds a scripted service the fuzzer SIGSTOPs while
// it starts: a start script is not interruptible (as in dinit), so a
// stop waits for the start timeout, 60s by default.
const ctlFuzzTimeouts = "start-timeout = 2\nstop-timeout = 1\n"

// ctlFuzzServices are written to the service directory. The variants
// in ctlFuzzRewrites replace them mid-run.
func ctlFuzzServices(dir string) map[string]string {
	script := filepath.Join(dir, "logged.sh")
	return map[string]string{
		"int-a": "type = internal\n",
		"proc": "type = process\ncommand = /bin/sleep 30\nstop-timeout = 1\n" +
			"reload-signal = HUP\nextra-command = act /bin/true\n" +
			"extra-command = act2 /bin/echo out\n",
		"scr":       "type = scripted\ncommand = /bin/true\nstop-command = /bin/true\n" + ctlFuzzTimeouts,
		"dep":       "type = internal\ndepends-on: int-a\nwaits-for: proc\n",
		"top":       "type = internal\ndepends-on: dep\ndepends-ms: scr\n",
		"trig":      "type = triggered\n",
		"needtrig":  "type = internal\ndepends-on: trig\n",
		"logged":    "type = process\ncommand = " + script + "\nlog-type = buffer\nlog-buffer-size = 4096\nstop-timeout = 1\n",
		"fail":      "type = scripted\ncommand = /bin/false\n" + ctlFuzzTimeouts,
		"depfail":   "type = internal\ndepends-on: fail\n",
		"broken":    "type = process\nno-such-setting = 1\n",
		"missdep":   "type = internal\ndepends-on: nowhere\n",
		"soft":      "type = internal\nwaits-for: fail\nwaits-for: proc\n",
		"logged.sh": "#!/bin/sh\necho hello from logged\nexec /bin/sleep 30\n",
	}
}

var ctlFuzzRewrites = []struct{ name, body string }{
	{"dep", "type = internal\ndepends-on: scr\n"},
	{"dep", "type = internal\ndepends-on: int-a\nwaits-for: proc\n"},
	{"dep", "type = process\ncommand = /bin/sleep 30\nstop-timeout = 1\ndepends-on: int-a\n"},
	{"int-a", "type = scripted\ncommand = /bin/true\n" + ctlFuzzTimeouts},
	{"int-a", "type = internal\n"},
	{"int-a", "type = internal\ndepends-on: top\n"}, // a cycle on disk
	{"proc", "type = internal\n"},
	{"proc", "type = process\ncommand = /bin/sleep 30\nstop-timeout = 1\nreload-signal = HUP\nextra-command = act /bin/true\n"},
	{"top", "this is not a setting\n"},
	{"top", "type = internal\ndepends-on: dep\ndepends-ms: scr\n"},
	{"trig", "type = internal\n"},
	{"trig", "type = triggered\n"},
}

var ctlFuzzNames = []string{
	"int-a", "proc", "scr", "dep", "top", "trig", "needtrig", "logged",
	"fail", "depfail", "broken", "missdep", "soft",
	"nowhere", "", ".hidden", "@x", "a/b", "../int-a", "proc@arg",
	"ünï", strings.Repeat("x", 300), "int-a\x00", "act", "boot",
}

var ctlFuzzStrings = []string{
	"", "A", "PATH", "A=B", "=", "K\x00", "\x00", "act", "act2", "nope",
	"HOME", strings.Repeat("v", 1000), "mem", "freeze", "/", "/nonexistent",
	`{"limit":5}`, `{"grep":"("}`, `{"grep":"x","limit":-1}`, "{", "null",
	`{"since":"bogus","unit":"proc"}`,
}

// ctlFuzzReader hands out the fuzz input a byte at a time, zero once it
// runs out, so every input decodes to some program.
type ctlFuzzReader struct {
	data []byte
	off  int
}

func (r *ctlFuzzReader) done() bool { return r.off >= len(r.data) }

func (r *ctlFuzzReader) byte() byte {
	if r.off >= len(r.data) {
		return 0
	}
	b := r.data[r.off]
	r.off++
	return b
}

func (r *ctlFuzzReader) u32() uint32 {
	return uint32(r.byte()) | uint32(r.byte())<<8 | uint32(r.byte())<<16 | uint32(r.byte())<<24
}

func (r *ctlFuzzReader) bytes(n int) []byte {
	out := make([]byte, 0, n)
	for i := 0; i < n && !r.done(); i++ {
		out = append(out, r.byte())
	}
	return out
}

// ctlFuzzConn is one client connection. A reader goroutine drains every
// packet the server sends, so the server never blocks on this client,
// and records the handles it hands out.
type ctlFuzzConn struct {
	conn net.Conn

	mu       sync.Mutex
	handles  []uint32
	versions int // RplyCPVersion replies seen
	dead     bool
	changed  chan struct{}
	readDone chan struct{}

	sentVersions int
	freed        []uint32
	maxHandle    uint32
}

func dialCtlFuzz(t *testing.T, sockPath string) *ctlFuzzConn {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &ctlFuzzConn{conn: conn, changed: make(chan struct{}, 1), readDone: make(chan struct{})}
	go c.readLoop()
	return c
}

func (c *ctlFuzzConn) readLoop() {
	defer close(c.readDone)
	for {
		typ, payload, err := ReadPacket(c.conn)
		c.mu.Lock()
		if err != nil {
			c.dead = true
		} else {
			switch typ {
			case RplyServiceRecord:
				if len(payload) >= 5 {
					h := binary.LittleEndian.Uint32(payload[1:5])
					c.addHandleLocked(h)
				}
			case RplyCPVersion:
				c.versions++
			}
		}
		c.mu.Unlock()
		select {
		case c.changed <- struct{}{}:
		default:
		}
		if err != nil {
			return
		}
	}
}

func (c *ctlFuzzConn) addHandleLocked(h uint32) {
	for _, have := range c.handles {
		if have == h {
			return
		}
	}
	c.handles = append(c.handles, h)
	if h > c.maxHandle {
		c.maxHandle = h
	}
}

// pickHandle turns a selector byte into a handle: mostly a live one,
// sometimes a closed one, zero, a never-allocated one or a huge one.
func (c *ctlFuzzConn) pickHandle(sel byte) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case sel < 0xC0 && len(c.handles) > 0:
		return c.handles[int(sel)%len(c.handles)]
	case sel < 0xD0 && len(c.freed) > 0:
		return c.freed[int(sel)%len(c.freed)]
	case sel < 0xE0:
		return 0
	case sel < 0xF0:
		return c.maxHandle + 1 + uint32(sel&0x0F)
	default:
		return 0xFFFFFFFF - uint32(sel&0x0F)
	}
}

func (c *ctlFuzzConn) forget(h uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, have := range c.handles {
		if have == h {
			c.handles = append(c.handles[:i], c.handles[i+1:]...)
			c.freed = append(c.freed, h)
			return
		}
	}
}

func (c *ctlFuzzConn) isDead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

// barrier sends a version query and waits for every version reply sent
// so far: replies come in order, so once it is in, the server has
// finished with every packet before it.
func (c *ctlFuzzConn) barrier() error {
	if err := WritePacket(c.conn, CmdQueryVersion, nil); err != nil {
		// The server may have closed the connection (a malformed
		// packet ends it) before the read loop has seen the EOF: the
		// write then fails with EPIPE first. Wait for the read loop to
		// say so rather than calling that a stuck server.
		select {
		case <-c.readDone:
		case <-time.After(opDeadline):
		}
		if c.isDead() {
			return nil
		}
		return fmt.Errorf("write barrier: %w", err)
	}
	c.sentVersions++
	deadline := time.NewTimer(opDeadline)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		got, dead := c.versions, c.dead
		c.mu.Unlock()
		if got >= c.sentVersions || dead {
			return nil
		}
		select {
		case <-c.changed:
		case <-deadline.C:
			return fmt.Errorf("no reply within %v", opDeadline)
		}
	}
}

func (c *ctlFuzzConn) close() {
	c.conn.Close()
	<-c.readDone
}

// buildPayload decodes a payload for cmd from the input. The shape byte
// picks the layout; most layouts start with something a handler parses
// (a handle, a name, a dep request) and the fuzzer controls the rest.
func buildPayload(r *ctlFuzzReader, c *ctlFuzzConn) []byte {
	shape := r.byte()
	var p []byte
	switch shape % 12 {
	case 0: // raw
		p = r.bytes(int(r.byte()))
	case 1: // handle + tail
		p = EncodeHandle(c.pickHandle(r.byte()))
		p = append(p, r.bytes(int(r.byte()%8))...)
	case 2: // name + tail
		p = EncodeServiceName(ctlFuzzNames[int(r.byte())%len(ctlFuzzNames)])
		p = append(p, r.bytes(int(r.byte()%4))...)
	case 3: // dep request: from, to, type
		p = EncodeDepRequest(c.pickHandle(r.byte()), c.pickHandle(r.byte()), r.byte()%8)
	case 4: // set-env
		key := ctlFuzzStrings[int(r.byte())%len(ctlFuzzStrings)]
		val := ctlFuzzStrings[int(r.byte())%len(ctlFuzzStrings)]
		p = EncodeSetEnv(c.pickHandle(r.byte()), key, val, r.byte()&1 != 0)
	case 5: // handle + u32 (signal number, from-handle)
		p = EncodeHandle(c.pickHandle(r.byte()))
		v := r.u32()
		if v&0x100 == 0 {
			v &= 0x3F // mostly a real signal number
		}
		p = binary.LittleEndian.AppendUint32(p, v)
	case 6: // byte + handle (catlog, rusage, shutdown type + flags)
		p = []byte{r.byte()}
		p = append(p, EncodeHandle(c.pickHandle(r.byte()))...)
	case 7: // empty
	case 8: // handle + name (run-action)
		p = EncodeHandle(c.pickHandle(r.byte()))
		p = append(p, EncodeServiceName(ctlFuzzStrings[int(r.byte())%len(ctlFuzzStrings)])...)
	case 9: // string payloads: journal JSON, wall, switch-root, suspend
		s := ctlFuzzStrings[int(r.byte())%len(ctlFuzzStrings)]
		switch r.byte() % 4 {
		case 0:
			p = []byte(s)
		case 1:
			p = EncodeServiceName(s)
		case 2:
			p = append(EncodeServiceName(s), EncodeServiceName(ctlFuzzStrings[int(r.byte())%len(ctlFuzzStrings)])...)
		case 3:
			p = append([]byte{byte(len(s))}, s...)
		}
	case 10: // schedule-shutdown: type, delay (BE), message
		p = []byte{r.byte() % 10}
		delay := r.u32()
		if delay&1 == 0 {
			delay = 3600 + delay%3600 // never fires during the run
		}
		p = binary.BigEndian.AppendUint32(p, delay)
		p = append(p, EncodeServiceName(ctlFuzzStrings[int(r.byte())%len(ctlFuzzStrings)])...)
	case 11: // oversized
		n := int(r.byte())<<8 | int(r.byte())
		p = make([]byte, n)
		fill := r.byte()
		for i := range p {
			p[i] = fill
		}
		if len(p) >= 4 {
			binary.LittleEndian.PutUint32(p, c.pickHandle(r.byte()))
		}
	}
	if len(p) > MaxPayloadSize {
		p = p[:MaxPayloadSize]
	}
	return p
}

// ctlFuzzStubs records what the stubs were asked to do; nothing is
// carried out.
type ctlFuzzStubs struct {
	mu        sync.Mutex
	shutdowns int
}

func runCtlFuzz(t *testing.T, data []byte) {
	baseGoroutines := runtime.NumGoroutine()
	// CTLFUZZ_FAILLOG=<file> appends every failure there as well. A
	// slow failing input can make the fuzz coordinator give up on the
	// worker while minimizing ("hung or terminated unexpectedly"), and
	// the failure it first saw is then printed nowhere.
	fatalf := func(format string, args ...any) {
		t.Helper()
		if path := os.Getenv("CTLFUZZ_FAILLOG"); path != "" {
			if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644); err == nil {
				fmt.Fprintf(f, "input %x\n"+format+"\n\n", append([]any{data}, args...)...)
				f.Close()
			}
		}
		t.Fatalf(format, args...)
	}

	dir := t.TempDir()
	svcDir := filepath.Join(dir, "services")
	if err := os.Mkdir(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range ctlFuzzServices(svcDir) {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(svcDir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}

	ss := service.NewServiceSet(&testLogger{})
	loader := config.NewDirLoader(ss, []string{svcDir})
	ss.SetLoader(loader)
	sockPath := filepath.Join(dir, "ctl.sock")
	server := NewServer(ss, sockPath, logging.New(logging.LevelError))
	stubs := &ctlFuzzStubs{}
	server.ShutdownFunc = func(service.ShutdownType, uint8) {
		stubs.mu.Lock()
		stubs.shutdowns++
		stubs.mu.Unlock()
	}
	server.WallFunc = func(service.ShutdownType, time.Duration, bool, string) {}
	server.WallReminderFunc = func(service.ShutdownType, time.Duration, string) {}
	server.WallNoticeFunc = func(string) {}
	server.SwitchRootFunc = func(string, string) error { return fmt.Errorf("stub") }
	server.SuspendFunc = func(state string) error {
		if state == "mem" {
			return nil
		}
		return fmt.Errorf("stub: %q", state)
	}
	server.NologinPath = filepath.Join(dir, "nologin")
	if err := server.Start(context.Background()); err != nil {
		fatalf("start server: %v", err)
	}
	stopped := false
	defer func() {
		if !stopped {
			ctlFuzzTeardown(ss)
			server.Stop()
		}
	}()

	conns := []*ctlFuzzConn{dialCtlFuzz(t, sockPath), dialCtlFuzz(t, sockPath)}
	defer func() {
		for _, c := range conns {
			c.close()
		}
	}()

	r := &ctlFuzzReader{data: data}
	var trace []string
	for ops := 0; ops < maxCtlFuzzOps && !r.done(); ops++ {
		sel := r.byte()
		ci := int(sel>>7) & 1
		c := conns[ci]
		if c.isDead() {
			// The server dropped this client: carry on with a new one.
			c.close()
			c = dialCtlFuzz(t, sockPath)
			conns[ci] = c
			trace = append(trace, fmt.Sprintf("c%d:reconnect", ci))
		}

		switch sel & 0x7F {
		case 0x7F: // rewrite a service description
			rw := ctlFuzzRewrites[int(r.byte())%len(ctlFuzzRewrites)]
			if err := os.WriteFile(filepath.Join(svcDir, rw.name), []byte(rw.body), 0o644); err != nil {
				t.Fatal(err)
			}
			trace = append(trace, fmt.Sprintf("rewrite %s=%q", rw.name, rw.body))
			continue
		case 0x7E: // a truncated frame, then hang up
			frame := r.bytes(1 + int(r.byte()%8))
			_, _ = c.conn.Write(frame)
			c.close()
			conns[ci] = dialCtlFuzz(t, sockPath)
			trace = append(trace, fmt.Sprintf("c%d:truncated %x", ci, frame))
			continue
		case 0x7D: // load a service by name, so later ops have handles
			name := ctlFuzzNames[int(r.byte())%len(ctlFuzzNames)]
			if err := WritePacket(c.conn, CmdLoadService, EncodeServiceName(name)); err != nil && !c.isDead() {
				fatalf("write: %v", err)
			}
			trace = append(trace, fmt.Sprintf("c%d:load %q", ci, name))
		default:
			cmd := ctlFuzzCmds[int(sel&0x7F)%len(ctlFuzzCmds)]
			payload := buildPayload(r, c)
			if cmd == CmdCloseHandle && len(payload) >= 4 {
				c.forget(binary.LittleEndian.Uint32(payload))
			}
			if cmd == CmdQueryVersion {
				c.sentVersions++
			}
			trace = append(trace, fmt.Sprintf("c%d:cmd %d %x", ci, cmd, ctlFuzzTrunc(payload)))
			if err := WritePacket(c.conn, cmd, payload); err != nil && !c.isDead() {
				fatalf("write: %v", err)
			}
		}
		if err := c.barrier(); err != nil {
			fatalf("op %d did not complete: %v\ntrace:\n  %s", ops, err, strings.Join(trace, "\n  "))
		}
	}

	// CTLFUZZ_DEBUG=1 logs what the input did, for working on a seed.
	if os.Getenv("CTLFUZZ_DEBUG") != "" {
		for _, c := range conns {
			c.mu.Lock()
			t.Logf("handles=%v versions=%d dead=%v", c.handles, c.versions, c.dead)
			c.mu.Unlock()
		}
		for _, s := range ss.ListServices() {
			t.Logf("svc %s %v", s.Name(), s.State())
		}
		t.Logf("trace:\n  %s", strings.Join(trace, "\n  "))
	}
	// The server must still serve a new client, including a command
	// that takes the graph lock and one that drives the state machine.
	if err := ctlFuzzLiveness(sockPath, ss); err != nil {
		fatalf("server wedged after input: %v\ntrace:\n  %s", err, strings.Join(trace, "\n  "))
	}

	if err := ctlFuzzInvariants(ss, true); err != nil {
		fatalf("invariant: %v\ntrace:\n  %s", err, strings.Join(trace, "\n  "))
	}

	// Tear down: clients first, then every service, then the server.
	for _, c := range conns {
		c.close()
	}
	conns = nil
	server.CancelShutdown()
	if err := ctlFuzzTeardown(ss); err != nil {
		fatalf("teardown: %v\ntrace:\n  %s", err, strings.Join(trace, "\n  "))
	}
	if err := ctlFuzzInvariants(ss, false); err != nil {
		fatalf("invariant after teardown: %v\ntrace:\n  %s", err, strings.Join(trace, "\n  "))
	}
	if n := ss.CountActiveServices(); n != 0 {
		var names []string
		for _, svc := range ss.ListServices() {
			names = append(names, fmt.Sprintf("%s(requiredBy %d)", svc.Name(), svc.Record().RequiredBy()))
		}
		fatalf("%d services still counted active after every service stopped; loaded: %v\ntrace:\n  %s",
			n, names, strings.Join(trace, "\n  "))
	}
	// Every client has gone, so none may still hold a restart open
	// (Connection.holdRestart): a hold left behind would keep a
	// container with nothing active from ever exiting. The server sees
	// each close on its own goroutine, hence the short wait.
	for deadline := time.Now().Add(2 * time.Second); ss.ClientRestartPending() && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if ss.ClientRestartPending() {
		fatalf("a client restart is still pending after every client closed\ntrace:\n  %s", strings.Join(trace, "\n  "))
	}
	stopped = true
	server.Stop()

	server.mu.Lock()
	left := len(server.conns)
	server.mu.Unlock()
	if left != 0 {
		fatalf("%d connections still registered after Stop", left)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= baseGoroutines+goroutineSlack {
			break
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			fatalf("goroutine leak: %d now, %d before\ntrace:\n  %s\n%s",
				n, baseGoroutines, strings.Join(trace, "\n  "), buf)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func ctlFuzzTrunc(p []byte) []byte {
	if len(p) > 24 {
		return p[:24]
	}
	return p
}

// ctlFuzzLiveness checks a new connection is answered: a version query,
// a list (graph lock), and a load + status + start of a service.
func ctlFuzzLiveness(sockPath string, ss *service.ServiceSet) error {
	errc := make(chan error, 1)
	go func() {
		conn, err := net.DialTimeout("unix", sockPath, opDeadline)
		if err != nil {
			errc <- fmt.Errorf("dial: %w", err)
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(opDeadline))
		next := func() (uint8, []byte, error) {
			for {
				typ, p, err := ReadPacket(conn)
				if err != nil {
					return 0, nil, err
				}
				if typ == InfoServiceEvent || typ == InfoServiceEvent5 || typ == InfoEnvEvent {
					continue
				}
				return typ, p, nil
			}
		}
		if err := WritePacket(conn, CmdQueryVersion, nil); err != nil {
			errc <- err
			return
		}
		if typ, _, err := next(); err != nil || typ != RplyCPVersion {
			errc <- fmt.Errorf("version: reply %d, %v", typ, err)
			return
		}
		if err := WritePacket(conn, CmdListServices5, nil); err != nil {
			errc <- err
			return
		}
		for {
			typ, _, err := next()
			if err != nil {
				errc <- fmt.Errorf("list: %v", err)
				return
			}
			if typ == RplyListDone {
				break
			}
		}
		errc <- nil
	}()
	select {
	case err := <-errc:
		if err != nil {
			return err
		}
	case <-time.After(2 * opDeadline):
		return fmt.Errorf("fresh connection not answered within %v", 2*opDeadline)
	}

	done := make(chan struct{})
	go func() {
		ss.ProcessQueues()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(opDeadline):
		return fmt.Errorf("ProcessQueues blocked (graph lock held) for %v", opDeadline)
	}
	return nil
}

// ctlFuzzInvariants checks the service set. With settle, it allows the
// state machine a moment to finish transitions in flight before the
// state-dependent checks count as failures.
func ctlFuzzInvariants(ss *service.ServiceSet, settle bool) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := ctlFuzzCheckOnce(ss)
		if err == nil || !settle || time.Now().After(deadline) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func ctlFuzzCheckOnce(ss *service.ServiceSet) error {
	svcs := ss.ListServices()
	var err error
	ss.WithGraphLock(func() {
		for _, svc := range svcs {
			rec := svc.Record()
			st := svc.State()
			if st > service.StateStopping {
				err = fmt.Errorf("%s: state %d out of range", svc.Name(), st)
				return
			}
			if ts := svc.TargetState(); ts != service.StateStopped && ts != service.StateStarted {
				err = fmt.Errorf("%s: target state %v not steady", svc.Name(), ts)
				return
			}
			if rec.RequiredBy() < 0 {
				err = fmt.Errorf("%s: requiredBy %d < 0", svc.Name(), rec.RequiredBy())
				return
			}
			held := 0
			for _, d := range rec.Dependents() {
				if d.HoldingAcq {
					held++
				}
			}
			if rec.IsMarkedActive() {
				held++
			}
			if rec.RequiredBy() != held {
				err = fmt.Errorf("%s: requiredBy %d, but %d acquisitions are held on it (explicit=%v)",
					svc.Name(), rec.RequiredBy(), held, rec.IsMarkedActive())
				return
			}
			if st == service.StateStarted {
				for _, d := range rec.Dependencies() {
					if d.DepType != service.DepRegular {
						continue
					}
					if ds := d.To.State(); ds != service.StateStarted {
						err = fmt.Errorf("%s is STARTED but its hard dependency %s is %v",
							svc.Name(), d.To.Name(), ds)
						return
					}
				}
			}
		}
	})
	return err
}

// ctlFuzzTeardown stops every service and waits for them to be down,
// so no child outlives the input.
func ctlFuzzTeardown(ss *service.ServiceSet) error {
	// Each service in turn, its pin dropped after the stop, as the
	// shutdown does: a stop put off by a pin is carried out when the
	// pin goes, the service STARTING or STARTED.
	for _, svc := range ss.ListServices() {
		// As `slinitctl stop --force` and dinit's stop: drop the
		// activation, then force.
		ss.StopService(svc)
		ss.ForceStopService(svc)
		ss.UnpinService(svc)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var up []string
		for _, svc := range ss.ListServices() {
			if svc.State() != service.StateStopped {
				rec := svc.Record()
				up = append(up, fmt.Sprintf("%s=%v(target %v, requiredBy %d, explicit %v, pinned %v)",
					svc.Name(), svc.State(), svc.TargetState(), rec.RequiredBy(),
					rec.IsMarkedActive(), rec.IsStartPinned()))
			}
		}
		if len(up) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("services not stopped after force-stop: %v", up)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ctlFuzzSeeds are hand-written programs: load everything, then the
// valid-looking sequences (start/stop/unload then reuse the stale
// handle, add-dep cycles, enable/disable, signals, catlog, env).
func ctlFuzzSeeds() [][]byte {
	idx := func(cmd uint8) byte {
		for i, c := range ctlFuzzCmds {
			if c == cmd {
				return byte(i)
			}
		}
		panic(cmd)
	}
	name := func(n string) byte {
		for i, s := range ctlFuzzNames {
			if s == n {
				return byte(i)
			}
		}
		panic(n)
	}
	loadAll := []byte{}
	for _, n := range []string{"int-a", "proc", "scr", "dep", "top", "trig", "needtrig", "logged", "fail", "soft"} {
		loadAll = append(loadAll, 0x7D, name(n))
	}
	h := func(i byte) byte { return i } // live-handle selector
	cat := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	op := func(cmd uint8, shape byte, rest ...byte) []byte {
		return append([]byte{idx(cmd), shape}, rest...)
	}
	return [][]byte{
		{},
		loadAll,
		// start top, stop, unload int-a, reuse handle 0.
		cat(loadAll,
			op(CmdStartService, 1, h(4), 0),
			op(CmdStartService, 1, h(1), 1, 0x01),
			op(CmdStopService, 1, h(0), 1, 0x02),
			op(CmdUnloadService, 1, h(0), 0),
			op(CmdCloseHandle, 1, h(0), 0),
			op(CmdStartService, 1, 0xC0, 0),
			op(CmdServiceStatus5, 1, h(0), 0)),
		// add-dep cycle int-a -> top, enable/disable.
		cat(loadAll,
			op(CmdAddDep, 3, h(0), h(4), 0),
			op(CmdAddDep, 3, h(4), h(0), 0),
			op(CmdEnableService, 5, h(2), 0, 0, 0, 0),
			op(CmdDisableServiceV7, 1, h(2), 0),
			op(CmdRmDepV7, 3, h(4), h(3), 0)),
		// signals and pause on the running process.
		cat(loadAll,
			op(CmdStartService, 1, h(1), 0),
			op(CmdSignal, 5, h(1), 0, 0, 0, 0),
			op(CmdSignal, 5, h(1), 19, 0, 0, 0),
			op(CmdPauseService, 1, h(1), 0),
			op(CmdSignal, 5, h(1), 0xFF, 0x01, 0xFF, 0x7F),
			op(CmdContinueService, 1, h(1), 0),
			op(CmdReloadSignal, 1, h(1), 0),
			op(CmdRunAction, 8, h(1), 8),
			op(CmdStopService, 1, h(1), 0)),
		// catlog, env, triggers.
		cat(loadAll,
			op(CmdStartService, 1, h(7), 0),
			op(CmdCatLog, 6, 1, h(7)),
			op(CmdSetEnv, 4, 3, 1, 0, 0),
			op(CmdSetEnv, 4, 2, 2, 0xD0, 0),
			op(CmdListenEnv, 7),
			op(CmdStartService, 1, h(6), 0),
			op(CmdSetTrigger, 1, h(5), 1, 1),
			op(CmdSetTrigger, 1, h(5), 1, 0),
			op(CmdGetAllEnv, 1, 0xD0, 0)),
		// reload after a dependency change on disk.
		cat(loadAll,
			op(CmdStartService, 1, h(4), 0),
			[]byte{0x7F, 0},
			op(CmdReloadService, 1, h(3), 0),
			[]byte{0x7F, 2},
			op(CmdReloadService, 1, h(3), 0),
			op(CmdReloadAll, 7),
			op(CmdStartAll, 7)),
		// shutdown family against the stubs.
		cat(loadAll,
			op(CmdScheduleShutdown, 10, 3, 0, 0, 0, 0, 0),
			op(CmdQueryShutdown, 7),
			op(CmdShutdown, 6, 2, 0),
			op(CmdCancelShutdown, 7),
			op(CmdSwitchRoot, 9, 14, 2, 15),
			op(CmdSuspend, 9, 12, 3),
			op(CmdWallNotice, 9, 1, 1)),
	}
}
