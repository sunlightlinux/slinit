package main

// Deferred set-up for mount units with `after:`. Each such unit gets a
// goroutine that watches the named slinit services over the control socket
// and, once every one of them is STARTED, hands the unit back to the main
// loop (ready queue + eventfd). The main loop does the actual autofs
// set-up, so fdMap/activeMounts stay single-goroutine.

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/autofs"
	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

const (
	defaultSystemSocket  = "/run/slinit.socket"
	defaultUserSocket    = ".slinitctl"
	controlRetryInterval = 5 * time.Second
)

// svcSession is one connection to slinit's control socket, seen as a source
// of service states. Tests substitute a fake.
type svcSession interface {
	// Load returns whether the named service is currently STARTED and
	// subscribes to its state changes.
	Load(name string) (started bool, err error)
	// Next blocks until a loaded service changes state.
	Next() (name string, started bool, err error)
	Close() error
}

type dialFunc func() (svcSession, error)

// pendingUnit is a mount unit waiting for its after: services.
type pendingUnit struct {
	unit *autofs.MountUnit
	stop chan struct{}
}

// waitServicesStarted blocks until every service in names is STARTED, as
// reported by sessions from dial, and returns true. It returns false only
// when stop is closed. Connection or lookup failures are logged (once per
// distinct error) and retried every retry interval.
func waitServicesStarted(dial dialFunc, names []string, stop <-chan struct{},
	retry time.Duration, logger *log.Logger, unitName string) bool {

	lastErr := ""
	report := func(err error) {
		if msg := err.Error(); msg != lastErr {
			lastErr = msg
			logger.Printf("%s: %v; retrying every %v", unitName, err, retry)
		}
	}
	for {
		select {
		case <-stop:
			return false
		default:
		}
		sess, err := dial()
		if err == nil {
			var ok bool
			ok, err = watchSession(sess, names, stop)
			if ok {
				return true
			}
		}
		if err != nil {
			report(err)
		}
		select {
		case <-stop:
			return false
		case <-time.After(retry):
		}
	}
}

// watchSession runs one session to completion: true once all names are
// STARTED, false with the error that ended the session (nil when stopped).
func watchSession(sess svcSession, names []string, stop <-chan struct{}) (bool, error) {
	// Closing the session is what unblocks Next when stop fires.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-stop:
		case <-done:
		}
		sess.Close()
	}()

	started := make(map[string]bool, len(names))
	for _, n := range names {
		s, err := sess.Load(n)
		if err != nil {
			return false, err
		}
		started[n] = s
	}
	for {
		all := true
		for _, n := range names {
			all = all && started[n]
		}
		if all {
			return true, nil
		}
		n, s, err := sess.Next()
		if err != nil {
			select {
			case <-stop:
				return false, nil
			default:
				return false, err
			}
		}
		started[n] = s
	}
}

// controlSession is the real svcSession over slinit's control socket.
type controlSession struct {
	conn   net.Conn
	names  map[uint32]string
	queued []svcState // events read while waiting for a reply
}

type svcState struct {
	name    string
	started bool
}

func dialControl(path string) dialFunc {
	return func() (svcSession, error) {
		conn, err := net.Dial("unix", path)
		if err != nil {
			return nil, fmt.Errorf("control socket: %w", err)
		}
		s := &controlSession{conn: conn, names: make(map[uint32]string)}
		if err := s.handshake(); err != nil {
			conn.Close()
			return nil, err
		}
		return s, nil
	}
}

func (s *controlSession) handshake() error {
	if err := control.WritePacket(s.conn, control.CmdQueryVersion, nil); err != nil {
		return fmt.Errorf("version handshake: %w", err)
	}
	rply, payload, err := control.ReadPacket(s.conn)
	if err != nil {
		return fmt.Errorf("version handshake: %w", err)
	}
	if rply != control.RplyCPVersion || len(payload) < 2 {
		return fmt.Errorf("version handshake: unexpected reply %d", rply)
	}
	// Payload is min(2)+actual(2); a lone version is the older form.
	ver := binary.LittleEndian.Uint16(payload)
	if len(payload) >= 4 {
		ver = binary.LittleEndian.Uint16(payload[2:])
	}
	if ver < control.MinCompatVersion {
		return fmt.Errorf("server protocol version %d is too old", ver)
	}
	return nil
}

// readReply reads the reply to the last command, queueing any service
// events that arrive first so Next still sees them.
func (s *controlSession) readReply() (uint8, []byte, error) {
	for {
		pkt, payload, err := control.ReadPacket(s.conn)
		if err != nil {
			return 0, nil, err
		}
		switch pkt {
		case control.InfoServiceEvent5:
			if st, ok := s.decodeEvent(payload); ok {
				s.queued = append(s.queued, st)
			}
		case control.InfoServiceEvent, control.InfoEnvEvent:
		default:
			return pkt, payload, nil
		}
	}
}

func (s *controlSession) decodeEvent(payload []byte) (svcState, bool) {
	h, _, status, err := control.DecodeServiceEvent5(payload)
	if err != nil {
		return svcState{}, false
	}
	name, ok := s.names[h]
	if !ok {
		return svcState{}, false
	}
	return svcState{name, status.State == service.StateStarted}, true
}

func (s *controlSession) Load(name string) (bool, error) {
	if err := control.WritePacket(s.conn, control.CmdLoadService, control.EncodeServiceName(name)); err != nil {
		return false, err
	}
	rply, data, err := s.readReply()
	if err != nil {
		return false, err
	}
	switch {
	case rply == control.RplyNoService:
		return false, fmt.Errorf("service %q not found", name)
	case rply != control.RplyServiceRecord || len(data) < 5:
		return false, fmt.Errorf("loading service %q: unexpected reply %d", name, rply)
	}
	s.names[binary.LittleEndian.Uint32(data[1:5])] = name
	return service.ServiceState(data[0]) == service.StateStarted, nil
}

func (s *controlSession) Next() (string, bool, error) {
	for {
		if len(s.queued) > 0 {
			st := s.queued[0]
			s.queued = s.queued[1:]
			return st.name, st.started, nil
		}
		pkt, payload, err := control.ReadPacket(s.conn)
		if err != nil {
			return "", false, err
		}
		if pkt != control.InfoServiceEvent5 {
			continue
		}
		if st, ok := s.decodeEvent(payload); ok {
			return st.name, st.started, nil
		}
	}
}

func (s *controlSession) Close() error {
	return s.conn.Close()
}

// resolveSocketPath picks the control socket of the slinit instance this
// process runs under, the way slinitctl does: the system socket for root,
// otherwise the user instance's.
func resolveSocketPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	// Same environment fallbacks as slinitctl.
	if p := os.Getenv("DINIT_SOCKET_PATH"); p != "" {
		return p
	}
	if p := os.Getenv("SLINIT_SOCKET_PATH"); p != "" {
		return p
	}
	if os.Getuid() == 0 {
		return defaultSystemSocket
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return xdg + "/slinitctl"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultUserSocket
	}
	return home + "/" + defaultUserSocket
}

// deferUnit starts waiting on mu's after: services; when they are all
// STARTED the unit is queued on d.ready and the main loop is woken.
func (d *daemon) deferUnit(mu *autofs.MountUnit) {
	pu := &pendingUnit{unit: mu, stop: make(chan struct{})}
	d.pending[mountUnitKey(mu)] = pu
	d.logger.Printf("%s (%s): waiting for service(s) to start: %s",
		mu.Name, mu.Where, strings.Join(mu.After, " "))
	go func() {
		if !waitServicesStarted(d.dial, mu.After, pu.stop, d.retry, d.logger, mu.Name) {
			return
		}
		d.readyMu.Lock()
		d.ready = append(d.ready, pu)
		d.readyMu.Unlock()
		d.wake()
	}()
}

// cancelPending stops the waiter of a pending unit and forgets it.
func (d *daemon) cancelPending(key string) {
	if pu, ok := d.pending[key]; ok {
		close(pu.stop)
		delete(d.pending, key)
	}
}

// wake nudges the epoll loop that a deferred unit is ready.
func (d *daemon) wake() {
	if d.readyFD < 0 {
		return
	}
	var one = [8]byte{1}
	if _, err := unix.Write(d.readyFD, one[:]); err != nil {
		d.logger.Printf("eventfd write: %v", err)
	}
}

// takeReady sets up every deferred unit whose services are all STARTED.
// Runs on the main loop only.
func (d *daemon) takeReady() {
	d.readyMu.Lock()
	ready := d.ready
	d.ready = nil
	d.readyMu.Unlock()
	for _, pu := range ready {
		key := mountUnitKey(pu.unit)
		if d.pending[key] != pu {
			continue // cancelled or replaced by a reload meanwhile
		}
		delete(d.pending, key)
		d.logger.Printf("%s: service(s) started: %s", pu.unit.Name, strings.Join(pu.unit.After, " "))
		d.setupUnit(pu.unit, "")
	}
}
