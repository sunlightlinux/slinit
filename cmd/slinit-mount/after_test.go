package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/autofs"
	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// fakeServices stands in for a slinit instance: a table of service states
// that sessions read and get change events from. Nothing here touches a
// real control socket or mounts anything.
type fakeServices struct {
	mu       sync.Mutex
	started  map[string]bool
	sessions []*fakeSession
	failDial int // dials to refuse before accepting
	dials    int
}

type fakeSession struct {
	src    *fakeServices
	events chan svcState
	closed chan struct{}
	once   sync.Once
}

func newFakeServices() *fakeServices {
	return &fakeServices{started: make(map[string]bool)}
}

func (f *fakeServices) dial() (svcSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if f.failDial > 0 {
		f.failDial--
		return nil, errors.New("control socket: connection refused")
	}
	s := &fakeSession{src: f, events: make(chan svcState, 64), closed: make(chan struct{})}
	f.sessions = append(f.sessions, s)
	return s, nil
}

func (f *fakeServices) set(name string, started bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started[name] = started
	for _, s := range f.sessions {
		s.events <- svcState{name, started}
	}
}

func (s *fakeSession) Load(name string) (bool, error) {
	s.src.mu.Lock()
	defer s.src.mu.Unlock()
	return s.src.started[name], nil
}

func (s *fakeSession) Next() (string, bool, error) {
	select {
	case ev := <-s.events:
		return ev.name, ev.started, nil
	case <-s.closed:
		return "", false, io.EOF
	}
}

func (s *fakeSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// startWait runs waitServicesStarted in the background; the result
// channel yields its return value.
func startWait(f *fakeServices, names []string, stop chan struct{}, logger *log.Logger) chan bool {
	res := make(chan bool, 1)
	go func() {
		res <- waitServicesStarted(f.dial, names, stop, 10*time.Millisecond, logger, "unit")
	}()
	return res
}

func expectPending(t *testing.T, res chan bool) {
	t.Helper()
	select {
	case r := <-res:
		t.Fatalf("waiter returned %v while a service is not started", r)
	case <-time.After(100 * time.Millisecond):
	}
}

func expectDone(t *testing.T, res chan bool, want bool) {
	t.Helper()
	select {
	case r := <-res:
		if r != want {
			t.Fatalf("waiter returned %v, want %v", r, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not return")
	}
}

func TestWaitUntilAllStarted(t *testing.T) {
	f := newFakeServices()
	f.started["net"] = true
	stop := make(chan struct{})
	res := startWait(f, []string{"net", "nfs"}, stop, quietLogger())

	expectPending(t, res)
	f.set("nfs", true)
	expectDone(t, res, true)
}

func TestWaitAlreadyStarted(t *testing.T) {
	f := newFakeServices()
	f.started["net"] = true
	res := startWait(f, []string{"net"}, make(chan struct{}), quietLogger())
	expectDone(t, res, true)
}

func TestWaitNeverStartedStaysPending(t *testing.T) {
	f := newFakeServices()
	f.started["net"] = true
	stop := make(chan struct{})
	res := startWait(f, []string{"net", "never"}, stop, quietLogger())

	expectPending(t, res)
	close(stop)
	expectDone(t, res, false)
}

// A service that started and stopped again does not count.
func TestWaitServiceStoppedAgain(t *testing.T) {
	f := newFakeServices()
	stop := make(chan struct{})
	defer close(stop)
	res := startWait(f, []string{"a", "b"}, stop, quietLogger())

	expectPending(t, res) // session loaded; the rest arrives as events
	f.set("a", true)
	f.set("a", false)
	f.set("b", true)
	expectPending(t, res)
	f.set("a", true)
	expectDone(t, res, true)
}

func TestWaitRetriesUnreachableSocket(t *testing.T) {
	f := newFakeServices()
	f.failDial = 3
	f.started["net"] = true
	var buf bytes.Buffer
	var bufMu sync.Mutex
	logger := log.New(writerFunc(func(p []byte) (int, error) {
		bufMu.Lock()
		defer bufMu.Unlock()
		return buf.Write(p)
	}), "", 0)

	expectDone(t, startWait(f, []string{"net"}, make(chan struct{}), logger), true)

	f.mu.Lock()
	dials := f.dials
	f.mu.Unlock()
	if dials != 4 {
		t.Errorf("dials = %d, want 4", dials)
	}
	bufMu.Lock()
	out := buf.String()
	bufMu.Unlock()
	if n := strings.Count(out, "connection refused"); n != 1 {
		t.Errorf("unreachable socket logged %d times, want once: %q", n, out)
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func newTestDaemon(t *testing.T, f *fakeServices, dirs ...string) *daemon {
	t.Helper()
	return &daemon{
		cfg:     &daemonConfig{mountDirs: dirs},
		logger:  quietLogger(),
		epfd:    -1,
		fdMap:   make(map[int]*mountInfo),
		pending: make(map[string]*pendingUnit),
		readyFD: -1,
		dial:    f.dial,
		retry:   10 * time.Millisecond,
	}
}

func stopAll(d *daemon) {
	for key := range d.pending {
		d.cancelPending(key)
	}
}

// With every unit waiting on after:, the daemon is not idle, so start-up
// does not give up with "no autofs mounts could be established".
func TestDaemonAllPendingIsNotIdle(t *testing.T) {
	d := newTestDaemon(t, newFakeServices())
	defer stopAll(d)
	d.addUnit(&autofs.MountUnit{Name: "a", Where: "/mnt/a", After: []string{"never"}}, "")
	d.addUnit(&autofs.MountUnit{Name: "b", Where: "/mnt/b", After: []string{"never"}}, "")

	if len(d.fdMap) != 0 {
		t.Fatalf("a unit with after: was set up immediately")
	}
	if len(d.pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(d.pending))
	}
	if d.idle() {
		t.Error("daemon with pending units reports idle")
	}
}

// Once its services start, a deferred unit is queued for the main loop.
// A unit cancelled (e.g. by a reload) in the meantime is dropped there.
func TestDeferredUnitQueuedWhenReady(t *testing.T) {
	f := newFakeServices()
	d := newTestDaemon(t, f)
	defer stopAll(d)
	mu := &autofs.MountUnit{Name: "a", Where: "/mnt/a", After: []string{"net"}}
	d.addUnit(mu, "")

	time.Sleep(50 * time.Millisecond)
	d.readyMu.Lock()
	early := len(d.ready)
	d.readyMu.Unlock()
	if early != 0 {
		t.Fatal("unit queued before its service started")
	}

	f.set("net", true)
	deadline := time.Now().Add(2 * time.Second)
	for {
		d.readyMu.Lock()
		n := len(d.ready)
		d.readyMu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unit not queued after its service started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Cancel before the loop picks it up: takeReady must not set it up
	// (which would also try a real autofs mount).
	d.cancelPending(mountUnitKey(mu))
	d.takeReady()
	if len(d.fdMap) != 0 || len(d.pending) != 0 {
		t.Errorf("cancelled unit was acted on: fdMap=%d pending=%d", len(d.fdMap), len(d.pending))
	}
}

func writeUnit(t *testing.T, dir, name, after string) {
	t.Helper()
	body := "what = tmpfs\nwhere = /mnt/" + name + "\ntype = tmpfs\nafter: " + after + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".mount"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestReloadDetectsAfterChange(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "x", "net")
	writeUnit(t, dir, "y", "net")
	d := newTestDaemon(t, newFakeServices(), dir)
	defer stopAll(d)

	units, err := autofs.LoadMountUnits([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		d.addUnit(u, "")
	}
	oldX, oldY := d.pending["/mnt/x"], d.pending["/mnt/y"]

	writeUnit(t, dir, "x", "net nfs")
	d.reloadConfig()

	newX := d.pending["/mnt/x"]
	if newX == nil || newX == oldX {
		t.Fatal("unit with changed after: was not re-deferred")
	}
	if got := strings.Join(newX.unit.After, " "); got != "net nfs" {
		t.Errorf("re-deferred after = %q, want %q", got, "net nfs")
	}
	select {
	case <-oldX.stop:
	default:
		t.Error("old waiter for the changed unit was not stopped")
	}
	if d.pending["/mnt/y"] != oldY {
		t.Error("unchanged pending unit was restarted")
	}

	os.Remove(filepath.Join(dir, "y.mount"))
	d.reloadConfig()
	if _, ok := d.pending["/mnt/y"]; ok {
		t.Error("removed pending unit still waiting")
	}
	select {
	case <-oldY.stop:
	default:
		t.Error("waiter for the removed unit was not stopped")
	}
}

// The real session against a scripted control-socket peer: an event that
// arrives while a load reply is awaited must still be delivered by Next.
func TestControlSessionQueuesEvents(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	event5 := func(h uint32, st service.ServiceState) []byte {
		b := make([]byte, 19)
		binary.LittleEndian.PutUint32(b, h)
		b[5] = uint8(st)
		return b
	}
	record := func(h uint32, st service.ServiceState) []byte {
		b := make([]byte, 6)
		b[0] = uint8(st)
		binary.LittleEndian.PutUint32(b[1:], h)
		return b
	}
	errc := make(chan error, 1)
	go func() {
		errc <- func() error {
			expect := func(want uint8) error {
				pkt, _, err := control.ReadPacket(server)
				if err == nil && pkt != want {
					err = fmt.Errorf("got packet %d, want %d", pkt, want)
				}
				return err
			}
			if err := expect(control.CmdQueryVersion); err != nil {
				return err
			}
			ver := make([]byte, 4)
			binary.LittleEndian.PutUint16(ver, control.MinCompatVersion)
			binary.LittleEndian.PutUint16(ver[2:], control.CPVersion)
			control.WritePacket(server, control.RplyCPVersion, ver)
			if err := expect(control.CmdLoadService); err != nil {
				return err
			}
			control.WritePacket(server, control.RplyServiceRecord, record(1, service.StateStopped))
			if err := expect(control.CmdLoadService); err != nil {
				return err
			}
			control.WritePacket(server, control.InfoServiceEvent5, event5(1, service.StateStarted))
			control.WritePacket(server, control.InfoServiceEvent, make([]byte, 17))
			control.WritePacket(server, control.RplyServiceRecord, record(2, service.StateStarted))
			control.WritePacket(server, control.InfoServiceEvent5, event5(2, service.StateStopped))
			return nil
		}()
	}()

	s := &controlSession{conn: client, names: make(map[uint32]string)}
	if err := s.handshake(); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Load("a"); err != nil || st {
		t.Fatalf("Load(a) = %v, %v; want stopped", st, err)
	}
	if st, err := s.Load("b"); err != nil || !st {
		t.Fatalf("Load(b) = %v, %v; want started", st, err)
	}
	if n, st, err := s.Next(); err != nil || n != "a" || !st {
		t.Fatalf("Next = %q %v %v; want queued a started", n, st, err)
	}
	if n, st, err := s.Next(); err != nil || n != "b" || st {
		t.Fatalf("Next = %q %v %v; want b stopped", n, st, err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestResolveSocketPathEnv(t *testing.T) {
	t.Setenv("DINIT_SOCKET_PATH", "")
	t.Setenv("SLINIT_SOCKET_PATH", "/tmp/s.sock")
	if got := resolveSocketPath(""); got != "/tmp/s.sock" {
		t.Errorf("SLINIT_SOCKET_PATH ignored: %q", got)
	}
	t.Setenv("DINIT_SOCKET_PATH", "/tmp/d.sock")
	if got := resolveSocketPath(""); got != "/tmp/d.sock" {
		t.Errorf("DINIT_SOCKET_PATH should win: %q", got)
	}
	if got := resolveSocketPath("/x.sock"); got != "/x.sock" {
		t.Errorf("-p should win: %q", got)
	}
}
