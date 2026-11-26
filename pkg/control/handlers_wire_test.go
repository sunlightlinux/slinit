package control

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// request sends one command and returns the first reply, skipping the
// unsolicited info packets.
func request(t *testing.T, conn net.Conn, cmd uint8, payload []byte) (uint8, []byte) {
	t.Helper()
	if err := WritePacket(conn, cmd, payload); err != nil {
		t.Fatalf("cmd %d: write: %v", cmd, err)
	}
	return readReply(t, conn)
}

// expectBadReq checks the malformed and unknown-handle forms every
// handle-taking command shares: no handle, a truncated handle, and a
// handle this connection never allocated.
func expectBadReq(t *testing.T, conn net.Conn, cmd uint8) {
	t.Helper()
	for name, p := range map[string][]byte{
		"empty":     nil,
		"truncated": {1, 0, 0},
		"unknown":   EncodeHandle(0xDEAD),
	} {
		if rply, _ := request(t, conn, cmd, p); rply != RplyBadReq {
			t.Errorf("cmd %d, %s handle: reply %d, want RplyBadReq", cmd, name, rply)
		}
	}
}

func handleName(t *testing.T, conn net.Conn, h uint32) string {
	t.Helper()
	rply, p := request(t, conn, CmdQueryServiceName, EncodeHandle(h))
	if rply != RplyServiceName {
		t.Fatalf("handle %d: name reply %d", h, rply)
	}
	name, _, err := DecodeServiceName(p)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// --- CmdQueryLoadMech ---

// decodeLoadMech parses loaderType(1) cwdLen(4) cwd numDirs(4) [len(4) dir]*
// and fails on any length that does not exactly fill the payload.
func decodeLoadMech(t *testing.T, p []byte) (uint8, string, []string) {
	t.Helper()
	if len(p) < 5 {
		t.Fatalf("load mech: %d bytes", len(p))
	}
	typ := p[0]
	cl := int(binary.LittleEndian.Uint32(p[1:]))
	off := 5
	if off+cl+4 > len(p) {
		t.Fatalf("load mech: cwd length %d overruns %d bytes", cl, len(p))
	}
	cwd := string(p[off : off+cl])
	off += cl
	n := int(binary.LittleEndian.Uint32(p[off:]))
	off += 4
	var dirs []string
	for i := 0; i < n; i++ {
		if off+4 > len(p) {
			t.Fatalf("load mech: dir %d header truncated", i)
		}
		dl := int(binary.LittleEndian.Uint32(p[off:]))
		off += 4
		if off+dl > len(p) {
			t.Fatalf("load mech: dir %d length %d overruns", i, dl)
		}
		dirs = append(dirs, string(p[off:off+dl]))
		off += dl
	}
	if off != len(p) {
		t.Fatalf("load mech: %d trailing bytes", len(p)-off)
	}
	return typ, cwd, dirs
}

func TestQueryLoadMechNoLoader(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	conn := connectTest(t, sockPath)
	defer conn.Close()

	rply, p := request(t, conn, CmdQueryLoadMech, nil)
	if rply != RplyLoaderMech {
		t.Fatalf("reply %d, want RplyLoaderMech", rply)
	}
	typ, cwd, dirs := decodeLoadMech(t, p)
	wd, _ := os.Getwd()
	if typ != 1 || cwd != wd || len(dirs) != 0 {
		t.Fatalf("got type=%d cwd=%q dirs=%q, want 1 %q []", typ, cwd, dirs, wd)
	}
}

func TestQueryLoadMechWithLoader(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	want := []string{"/etc/slinit.d", "/run/slinit.d", "/lib/slinit.d"}
	server.services.SetLoader(config.NewDirLoader(server.services, want))
	conn := connectTest(t, sockPath)
	defer conn.Close()

	rply, p := request(t, conn, CmdQueryLoadMech, nil)
	if rply != RplyLoaderMech {
		t.Fatalf("reply %d, want RplyLoaderMech", rply)
	}
	typ, cwd, dirs := decodeLoadMech(t, p)
	wd, _ := os.Getwd()
	if typ != 1 || cwd != wd || !reflect.DeepEqual(dirs, want) {
		t.Fatalf("got type=%d cwd=%q dirs=%q, want 1 %q %q", typ, cwd, dirs, wd, want)
	}
}

// --- CmdQueryDependents ---

func TestQueryDependents(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	for _, n := range []string{"dep-base", "dep-user1", "dep-user2"} {
		server.services.AddService(service.NewInternalService(server.services, n))
	}
	conn := connectTest(t, sockPath)
	defer conn.Close()

	base := findHandle(t, conn, "dep-base")
	u1 := findHandle(t, conn, "dep-user1")
	u2 := findHandle(t, conn, "dep-user2")
	for _, h := range []uint32{u1, u2} {
		if rply, _ := request(t, conn, CmdAddDep, EncodeDepRequest(h, base, uint8(service.DepRegular))); rply != RplyACK {
			t.Fatalf("add-dep: reply %d", rply)
		}
	}

	rply, p := request(t, conn, CmdQueryDependents, EncodeHandle(base))
	if rply != RplyDependents {
		t.Fatalf("reply %d, want RplyDependents", rply)
	}
	if len(p) != 4+4*2 {
		t.Fatalf("payload %d bytes, want 12", len(p))
	}
	if n := binary.LittleEndian.Uint32(p); n != 2 {
		t.Fatalf("count %d, want 2", n)
	}
	var names []string
	for off := 4; off < len(p); off += 4 {
		h := binary.LittleEndian.Uint32(p[off:])
		// Handles already held for a service are reused, not duplicated.
		if h != u1 && h != u2 {
			t.Errorf("dependent handle %d is neither %d nor %d", h, u1, u2)
		}
		names = append(names, handleName(t, conn, h))
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"dep-user1", "dep-user2"}) {
		t.Fatalf("dependents %q", names)
	}

	// A service nothing depends on: count 0 and nothing else.
	rply, p = request(t, conn, CmdQueryDependents, EncodeHandle(u1))
	if rply != RplyDependents || len(p) != 4 || binary.LittleEndian.Uint32(p) != 0 {
		t.Fatalf("leaf: reply %d payload %v, want RplyDependents [0 0 0 0]", rply, p)
	}

	expectBadReq(t, conn, CmdQueryDependents)
}

// A dependent the client never looked up gets a fresh handle that the
// connection then resolves like any other.
func TestQueryDependentsAllocatesHandles(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	base := service.NewInternalService(server.services, "alloc-base")
	user := service.NewInternalService(server.services, "alloc-user")
	server.services.AddService(base)
	server.services.AddService(user)
	user.Record().AddDep(base, service.DepRegular)

	conn := connectTest(t, sockPath)
	defer conn.Close()
	hb := findHandle(t, conn, "alloc-base")

	rply, p := request(t, conn, CmdQueryDependents, EncodeHandle(hb))
	if rply != RplyDependents || len(p) != 8 || binary.LittleEndian.Uint32(p) != 1 {
		t.Fatalf("reply %d payload %v", rply, p)
	}
	h := binary.LittleEndian.Uint32(p[4:])
	if h == hb {
		t.Fatalf("dependent got the queried service's own handle %d", h)
	}
	if got := handleName(t, conn, h); got != "alloc-user" {
		t.Fatalf("dependent %q, want alloc-user", got)
	}
}

// --- CmdFreezeService / CmdThawService ---

func TestFreezeThawService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	cg := t.TempDir()
	svc := service.NewInternalService(server.services, "freeze-me")
	svc.Record().SetCgroupPath(cg)
	server.services.AddService(svc)
	server.services.AddService(service.NewInternalService(server.services, "no-cgroup"))

	conn := connectTest(t, sockPath)
	defer conn.Close()
	h := findHandle(t, conn, "freeze-me")
	freezeFile := filepath.Join(cg, "cgroup.freeze")

	for _, step := range []struct {
		cmd  uint8
		want string
	}{{CmdFreezeService, "1"}, {CmdThawService, "0"}, {CmdFreezeService, "1"}} {
		if rply, _ := request(t, conn, step.cmd, EncodeHandle(h)); rply != RplyACK {
			t.Fatalf("cmd %d: reply %d, want ACK", step.cmd, rply)
		}
		data, err := os.ReadFile(freezeFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != step.want {
			t.Fatalf("cmd %d: cgroup.freeze = %q, want %q", step.cmd, data, step.want)
		}
	}

	// No cgroup of its own: the write cannot happen, the client hears NAK.
	hn := findHandle(t, conn, "no-cgroup")
	for _, cmd := range []uint8{CmdFreezeService, CmdThawService} {
		if rply, _ := request(t, conn, cmd, EncodeHandle(hn)); rply != RplyNAK {
			t.Errorf("cmd %d without cgroup: reply %d, want NAK", cmd, rply)
		}
	}

	// A cgroup path that does not exist: NAK, and nothing is created.
	gone := filepath.Join(cg, "missing")
	svc.Record().SetCgroupPath(gone)
	if rply, _ := request(t, conn, CmdFreezeService, EncodeHandle(h)); rply != RplyNAK {
		t.Errorf("missing cgroup dir: reply %d, want NAK", rply)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("missing cgroup dir was created: %v", err)
	}

	expectBadReq(t, conn, CmdFreezeService)
	expectBadReq(t, conn, CmdThawService)
}

// --- CmdQueryDescription ---

func TestQueryDescription(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	svc := service.NewInternalService(server.services, "described")
	svc.Record().SetDescription("The thing that does the work")
	server.services.AddService(svc)
	server.services.AddService(service.NewInternalService(server.services, "undescribed"))
	conn := connectTest(t, sockPath)
	defer conn.Close()

	for name, want := range map[string]string{
		"described":   "The thing that does the work",
		"undescribed": "",
	} {
		rply, p := request(t, conn, CmdQueryDescription, EncodeHandle(findHandle(t, conn, name)))
		if rply != RplyDescription {
			t.Fatalf("%s: reply %d, want RplyDescription", name, rply)
		}
		got, n, err := DecodeServiceName(p)
		if err != nil || n != len(p) || got != want {
			t.Fatalf("%s: description %q (n=%d of %d, err %v), want %q", name, got, n, len(p), err, want)
		}
	}
	expectBadReq(t, conn, CmdQueryDescription)
}

// --- CmdQueryBundleMembers ---

func TestQueryBundleMembers(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	b := service.NewInternalService(server.services, "a-bundle")
	b.Record().SetBundleMembers([]string{"m1", "m2", "m3"})
	server.services.AddService(b)
	server.services.AddService(service.NewInternalService(server.services, "not-a-bundle"))
	conn := connectTest(t, sockPath)
	defer conn.Close()

	for name, want := range map[string][]string{
		"a-bundle":     {"m1", "m2", "m3"},
		"not-a-bundle": {},
	} {
		rply, p := request(t, conn, CmdQueryBundleMembers, EncodeHandle(findHandle(t, conn, name)))
		if rply != RplyBundleMembers {
			t.Fatalf("%s: reply %d, want RplyBundleMembers", name, rply)
		}
		got, n, err := DecodeStringList(p)
		if err != nil || n != len(p) || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: members %q (n=%d of %d, err %v), want %q", name, got, n, len(p), err, want)
		}
	}
	expectBadReq(t, conn, CmdQueryBundleMembers)
}

// --- CmdListActions ---

func TestListActions(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	svc := service.NewInternalService(server.services, "with-actions")
	svc.Record().SetExtraCommands(map[string][]string{"rotate": {"/bin/true"}})
	server.services.AddService(svc)
	server.services.AddService(service.NewInternalService(server.services, "no-actions"))
	conn := connectTest(t, sockPath)
	defer conn.Close()

	rply, p := request(t, conn, CmdListActions, EncodeHandle(findHandle(t, conn, "with-actions")))
	if rply != RplyActionList || string(p) != "rotate" {
		t.Fatalf("reply %d %q, want RplyActionList \"rotate\"", rply, p)
	}
	rply, p = request(t, conn, CmdListActions, EncodeHandle(findHandle(t, conn, "no-actions")))
	if rply != RplyActionList || len(p) != 0 {
		t.Fatalf("no actions: reply %d %q, want empty RplyActionList", rply, p)
	}
	expectBadReq(t, conn, CmdListActions)
}

// --- profiles: CmdQueryProfile, CmdListProfiles, CmdActivateProfile ---

func TestProfilesOverTheWire(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	mk := func(name string, profiles ...string) service.Service {
		s := service.NewInternalService(server.services, name)
		s.Record().SetProfiles(profiles)
		server.services.AddService(s)
		return s
	}
	mk("p-home", "home")
	mk("p-work", "work")
	mk("p-both", "home", "work")
	global := mk("p-global")

	conn := connectTest(t, sockPath)
	defer conn.Close()

	queryProfile := func() string {
		t.Helper()
		rply, p := request(t, conn, CmdQueryProfile, nil)
		if rply != RplyProfile {
			t.Fatalf("query profile: reply %d", rply)
		}
		name, _, err := DecodeServiceName(p)
		if err != nil {
			t.Fatal(err)
		}
		return name
	}

	if got := queryProfile(); got != "" {
		t.Fatalf("initial profile %q, want none", got)
	}

	rply, p := request(t, conn, CmdListProfiles, nil)
	if rply != RplyProfileList {
		t.Fatalf("list profiles: reply %d", rply)
	}
	if got, n, err := DecodeStringList(p); err != nil || n != len(p) || !reflect.DeepEqual(got, []string{"home", "work"}) {
		t.Fatalf("profiles %q (n=%d of %d, err %v), want [home work]", got, n, len(p), err)
	}

	// A profile nobody declares is refused with the reason, and changes nothing.
	rply, p = request(t, conn, CmdActivateProfile, EncodeServiceName("nope"))
	if rply != RplyNAK || !strings.Contains(string(p), "nope") {
		t.Fatalf("unknown profile: reply %d %q, want NAK naming it", rply, p)
	}
	if got := queryProfile(); got != "" {
		t.Fatalf("profile after refused activation %q", got)
	}

	// Malformed names never reach the service set.
	for name, payload := range map[string][]byte{
		"empty":     nil,
		"short len": {5},
		"overlong":  {9, 0, 'w', 'o', 'r', 'k'},
		"nul":       {2, 0, 'w', 0},
	} {
		if rply, _ := request(t, conn, CmdActivateProfile, payload); rply != RplyBadReq {
			t.Errorf("%s: reply %d, want RplyBadReq", name, rply)
		}
	}
	if got := queryProfile(); got != "" {
		t.Fatalf("profile after malformed activation %q", got)
	}

	rply, p = request(t, conn, CmdActivateProfile, EncodeServiceName("home"))
	if rply != RplyActivateResult {
		t.Fatalf("activate home: reply %d %q", rply, p)
	}
	active, stopped, started, kept, err := DecodeActivateResult(p)
	if err != nil {
		t.Fatal(err)
	}
	// Leaving "no profile", nothing was in the old set: p-work is
	// neither stopped nor started, it is kept as it was.
	if active != "home" || len(stopped) != 0 ||
		!reflect.DeepEqual(started, []string{"p-both", "p-home"}) || !reflect.DeepEqual(kept, []string{"p-work"}) {
		t.Fatalf("activate home: %q stopped=%q started=%q kept=%q", active, stopped, started, kept)
	}
	if got := queryProfile(); got != "home" {
		t.Fatalf("profile %q, want home", got)
	}

	rply, p = request(t, conn, CmdActivateProfile, EncodeServiceName("work"))
	if rply != RplyActivateResult {
		t.Fatalf("activate work: reply %d %q", rply, p)
	}
	active, stopped, started, kept, err = DecodeActivateResult(p)
	if err != nil {
		t.Fatal(err)
	}
	if active != "work" || !reflect.DeepEqual(stopped, []string{"p-home"}) ||
		!reflect.DeepEqual(started, []string{"p-work"}) || !reflect.DeepEqual(kept, []string{"p-both"}) {
		t.Fatalf("activate work: %q stopped=%q started=%q kept=%q", active, stopped, started, kept)
	}
	if got := queryProfile(); got != "work" {
		t.Fatalf("profile %q, want work", got)
	}
	// The services' resulting states are ServiceSet.ActivateProfile's
	// business and are not pinned here; only that a global service is
	// never moved by a swap.
	if global.State() != service.StateStopped {
		t.Fatalf("global service was touched: %v", global.State())
	}
}

// --- scheduled shutdown: CmdScheduleShutdown, CmdQueryShutdown, CmdCancelShutdown ---

func TestScheduledShutdownOverTheWire(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	fired := make(chan service.ShutdownType, 4)
	server.ShutdownFunc = func(st service.ShutdownType, _ uint8) { fired <- st }
	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Nothing pending yet.
	if rply, _ := request(t, conn, CmdQueryShutdown, nil); rply != RplyNAK {
		t.Fatalf("query with none pending: reply %d, want NAK", rply)
	}
	if rply, _ := request(t, conn, CmdCancelShutdown, nil); rply != RplyNAK {
		t.Fatalf("cancel with none pending: reply %d, want NAK", rply)
	}

	// Malformed requests schedule nothing.
	for name, p := range map[string][]byte{
		"empty":    nil,
		"short":    {byte(service.ShutdownReboot), 0, 0, 0},
		"bad type": {0xEE, 0, 0, 0x0E, 0x10},
	} {
		if rply, _ := request(t, conn, CmdScheduleShutdown, p); rply != RplyBadReq {
			t.Errorf("%s: reply %d, want RplyBadReq", name, rply)
		}
	}
	if rply, _ := request(t, conn, CmdQueryShutdown, nil); rply != RplyNAK {
		t.Fatalf("query after malformed schedule: reply %d, want NAK", rply)
	}

	p, err := EncodeScheduleShutdown(service.ShutdownReboot, time.Hour, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if rply, _ := request(t, conn, CmdScheduleShutdown, p); rply != RplyACK {
		t.Fatalf("schedule: reply %d, want ACK", rply)
	}

	rply, q := request(t, conn, CmdQueryShutdown, nil)
	if rply != RplyShutdownStatus || len(q) != 5 {
		t.Fatalf("query: reply %d payload %v, want RplyShutdownStatus with 5 bytes", rply, q)
	}
	if service.ShutdownType(q[0]) != service.ShutdownReboot {
		t.Errorf("type %d, want reboot", q[0])
	}
	// remaining_secs is big-endian, like the request's delay.
	secs := binary.BigEndian.Uint32(q[1:])
	if secs > 3600 || secs < 3500 {
		t.Errorf("remaining %d s, want just under 3600", secs)
	}

	if rply, _ := request(t, conn, CmdCancelShutdown, nil); rply != RplyACK {
		t.Fatalf("cancel: reply %d, want ACK", rply)
	}
	if rply, _ := request(t, conn, CmdQueryShutdown, nil); rply != RplyNAK {
		t.Fatalf("query after cancel: reply %d, want NAK", rply)
	}
	if rply, _ := request(t, conn, CmdCancelShutdown, nil); rply != RplyNAK {
		t.Fatalf("second cancel: reply %d, want NAK", rply)
	}
	select {
	case st := <-fired:
		t.Fatalf("shutdown %d ran for a cancelled schedule", st)
	default:
	}

	// A zero delay is immediate.
	p, _ = EncodeScheduleShutdown(service.ShutdownPoweroff, 0, "")
	if rply, _ := request(t, conn, CmdScheduleShutdown, p); rply != RplyACK {
		t.Fatalf("immediate: reply %d, want ACK", rply)
	}
	select {
	case st := <-fired:
		if st != service.ShutdownPoweroff {
			t.Fatalf("immediate shutdown type %d, want poweroff", st)
		}
	default:
		t.Fatal("immediate schedule did not call ShutdownFunc before replying")
	}
}

// --- CmdWallNotice ---

func TestWallNoticeOverTheWire(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	got := make(chan string, 4)
	server.WallNoticeFunc = func(m string) { got <- m }
	conn := connectTest(t, sockPath)
	defer conn.Close()

	msg := "back in five"
	p := append([]byte{byte(len(msg)), 0}, msg...)
	p = append(p, "trailing"...) // past the declared length: ignored
	if rply, _ := request(t, conn, CmdWallNotice, p); rply != RplyACK {
		t.Fatalf("wall: reply %d, want ACK", rply)
	}
	select {
	case m := <-got:
		if m != msg {
			t.Fatalf("wall message %q, want %q", m, msg)
		}
	default:
		t.Fatal("WallNoticeFunc not called")
	}

	for name, p := range map[string][]byte{
		"empty":     nil,
		"one byte":  {3},
		"zero len":  {0, 0},
		"overlong":  {5, 0, 'a', 'b', 'c', 'd'},
		"high byte": {1, 1, 'a'},
	} {
		if rply, _ := request(t, conn, CmdWallNotice, p); rply != RplyBadReq {
			t.Errorf("%s: reply %d, want RplyBadReq", name, rply)
		}
	}
	select {
	case m := <-got:
		t.Fatalf("malformed notice was broadcast: %q", m)
	default:
	}

	// Without a broadcaster wired the request still succeeds.
	server.WallNoticeFunc = nil
	if rply, _ := request(t, conn, CmdWallNotice, []byte{1, 0, 'x'}); rply != RplyACK {
		t.Fatalf("no broadcaster: reply %d, want ACK", rply)
	}
}

// --- HandlePassCSFD / HeldByClient ---

// A pre-connected control socket (--pass-cs-fd) is served like an
// accepted one: commands work and its handles count as held.
func TestHandlePassCSFD(t *testing.T) {
	server, _ := setupTestServer(t)
	defer server.Stop()
	svc := service.NewInternalService(server.services, "held")
	server.services.AddService(svc)

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	mkConn := func(fd int, name string) net.Conn {
		f := os.NewFile(uintptr(fd), name)
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	daemonSide := mkConn(fds[0], "daemon")
	client := mkConn(fds[1], "client")
	defer client.Close()

	server.HandlePassCSFD(daemonSide)

	if rply, _ := request(t, client, CmdQueryVersion, nil); rply != RplyCPVersion {
		t.Fatalf("version over passed fd: reply %d", rply)
	}
	if server.HeldByClient(svc) {
		t.Fatal("held before any handle was taken")
	}
	h := findHandle(t, client, "held")
	if !server.HeldByClient(svc) {
		t.Fatal("not held after find")
	}
	if rply, _ := request(t, client, CmdCloseHandle, EncodeHandle(h)); rply != RplyACK {
		t.Fatalf("close handle: reply %d", rply)
	}
	if server.HeldByClient(svc) {
		t.Fatal("still held after the handle was closed")
	}

	// Closing the client ends the connection's goroutine; the server
	// forgets it, so Stop does not wait on it forever.
	client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		server.mu.Lock()
		n := len(server.conns)
		server.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connection(s) still registered after client closed", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
