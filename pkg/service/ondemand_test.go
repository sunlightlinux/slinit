package service

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newOnDemand builds an on-demand service listening on addr.
func newOnDemand(t *testing.T, set *ServiceSet, name, addr string, cmd []string) *ProcessService {
	t.Helper()
	svc := NewProcessService(set, name)
	svc.SetCommand(cmd)
	svc.Record().SetSocketDetails(addr, 0600, -1, -1)
	svc.Record().SetSocketPaths([]string{addr})
	svc.SetSocketOnDemand(true)
	set.AddService(svc)
	return svc
}

func stopAndWait(t *testing.T, set *ServiceSet, svc Service) {
	t.Helper()
	set.StopService(svc)
	waitState(t, svc, StateStopped, 5*time.Second)
}

// Starting an on-demand service opens the socket and reports STARTED,
// with no process: the socket accepting is what a dependent needs.
func TestOnDemandListensWithoutProcess(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "od.sock")
	set, _ := newTestSet()
	svc := newOnDemand(t, set, "od-idle", sock, []string{"/bin/sleep", "60"})
	defer stopAndWait(t, set, svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}
	time.Sleep(200 * time.Millisecond)
	if pid := svc.PID(); pid != 0 {
		t.Errorf("process launched before any client (pid %d)", pid)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("socket not listening: %v", err)
	}
}

// The first client launches the process and is served by it: the watcher
// must not consume the connection. After the process exits, the service
// stays STARTED and the next client launches it again.
func TestOnDemandFirstClientIsServedAndRearms(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "od.sock")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	set, _ := newTestSet()
	svc := newOnDemand(t, set, "od-serve", sock,
		[]string{"/usr/bin/env", acceptOneEnv + "=1", self})
	defer stopAndWait(t, set, svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}

	for round := 1; round <= 2; round++ {
		conn, err := net.DialTimeout("unix", sock, 2*time.Second)
		if err != nil {
			t.Fatalf("round %d: dial: %v", round, err)
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if err != nil || line != "hello\n" {
			t.Fatalf("round %d: client not served: %q, %v", round, line, err)
		}

		// The helper exits after one client; the service must stay up
		// and go back to listening.
		deadline := time.Now().Add(3 * time.Second)
		for svc.PID() != 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		if st := svc.State(); st != StateStarted {
			t.Fatalf("round %d: state after process exit = %v, want STARTED", round, st)
		}
		if pid := svc.PID(); pid != 0 {
			t.Fatalf("round %d: process still running (pid %d)", round, pid)
		}
	}
}

// A datagram socket activates too; Accept-based watching could not.
func TestOnDemandUDPActivates(t *testing.T) {
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()
	addr := "udp:127.0.0.1:" + strconv.Itoa(port)

	marker := filepath.Join(t.TempDir(), "launched")
	set, _ := newTestSet()
	svc := newOnDemand(t, set, "od-udp", addr,
		[]string{"/bin/sh", "-c", "touch " + marker + "; sleep 60"})
	defer stopAndWait(t, set, svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("launched before any datagram")
	}

	c, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("ping"))
	c.Close()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("datagram did not launch the process")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Stopping closes the socket and disarms the watcher: a later client
// must neither connect nor launch anything.
func TestOnDemandStopClosesSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "od.sock")
	marker := filepath.Join(t.TempDir(), "launched")
	set, _ := newTestSet()
	svc := newOnDemand(t, set, "od-stop", sock,
		[]string{"/bin/sh", "-c", "touch " + marker + "; sleep 60"})

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}
	stopAndWait(t, set, svc)

	if _, err := net.DialTimeout("unix", sock, 500*time.Millisecond); err == nil {
		t.Error("socket still accepting after stop")
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("process launched after the service was stopped")
	}
}

// watchdog-timeout must cover a process launched on demand. The watchdog
// was armed only on the STARTING → STARTED readiness transition, which
// an on-demand launch never makes (the service is STARTED from the
// moment its sockets open), so such a process ran unwatched. A missed
// keepalive kills the process; the service stays up and listens again.
func TestOnDemandWatchdogCoversLaunchedProcess(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "od.sock")
	set, _ := newTestSet()
	// Signals readiness once, then never sends a keepalive.
	svc := newOnDemand(t, set, "od-watchdog", sock,
		[]string{"/bin/sh", "-c", `echo x >&$NOTIFY_FD; sleep 60`})
	svc.SetReadyNotification(-1, "NOTIFY_FD")
	svc.SetWatchdogTimeout(300 * time.Millisecond)
	defer stopAndWait(t, set, svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for svc.PID() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	first := svc.PID()
	if first == 0 {
		t.Fatal("process never launched")
	}
	// Within a few watchdog periods the silent process must be killed.
	// The client is still waiting in the backlog (sh never accepts it),
	// so the service, still STARTED, launches a fresh process for it.
	deadline = time.Now().Add(3 * time.Second)
	for svc.PID() == first && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if svc.PID() == first {
		t.Fatalf("process %d still running: the watchdog never fired", first)
	}
	if st := svc.State(); st != StateStarted {
		t.Errorf("state after watchdog = %v, want STARTED (listening)", st)
	}
}

// A client left in the backlog of a service whose process exits without
// accepting it re-arms the watcher on a socket that is still readable:
// PID 1 launched the process again at once, without end. One connect
// from any user who may reach the socket — and a process that fails
// before it accepts, a missing binary, a bad config — kept PID 1 forking
// and logging in a tight loop, holding queueMu for every turn. systemd
// bounds this with the socket's trigger limit (TriggerLimitBurst=20 in
// TriggerLimitIntervalSec=2s) and fails the socket when it is hit.
func TestOnDemandTriggerLimitStopsLaunchLoop(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "od.sock")
	count := filepath.Join(t.TempDir(), "launches")
	set, _ := newTestSet()
	svc := newOnDemand(t, set, "od-loop", sock,
		[]string{"/bin/sh", "-c", "echo x >> " + count + "; exit 1"})
	defer func() {
		set.StopService(svc)
		waitState(t, svc, StateStopped, 5*time.Second)
	}()

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	time.Sleep(1500 * time.Millisecond)
	b, _ := os.ReadFile(count)
	launches := len(b) / 2
	if launches > onDemandTriggerBurst {
		t.Fatalf("one client launched the process %d times in 1.5s (limit %d)",
			launches, onDemandTriggerBurst)
	}
	if st := waitState(t, svc, StateStopped, 3*time.Second); st != StateStopped {
		t.Errorf("state after trigger limit = %v, want STOPPED", st)
	}
}

// A healthy service that serves one client per launch and exits 0 must
// survive a burst of clients: the trigger limit is there for a process
// that fails before it accepts, not for one doing its job. systemd fails
// the socket here (it counts every launch, with TriggerLimitBurst= to
// raise it); slinit has no such directive, so only failed launches count.
func TestOnDemandBurstOfClientsKeepsHealthyService(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "od.sock")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	set, _ := newTestSet()
	svc := newOnDemand(t, set, "od-burst", sock,
		[]string{"/usr/bin/env", acceptOneEnv + "=1", self})
	defer stopAndWait(t, set, svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}
	const clients = onDemandTriggerBurst + 5
	served := make(chan bool, clients)
	for i := 0; i < clients; i++ {
		go func() {
			conn, err := net.DialTimeout("unix", sock, 2*time.Second)
			if err != nil {
				served <- false
				return
			}
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(20 * time.Second))
			line, err := bufio.NewReader(conn).ReadString('\n')
			served <- err == nil && line == "hello\n"
		}()
	}
	n := 0
	for i := 0; i < clients; i++ {
		if <-served {
			n++
		}
	}
	if n != clients {
		t.Errorf("served %d of %d clients", n, clients)
	}
	if st := svc.State(); st != StateStarted {
		t.Errorf("state after a burst of %d clients = %v, want STARTED", clients, st)
	}
}

// Each on-demand service's watcher waited for its first client in
// poll(2), holding an OS thread until then: 200 idle on-demand services
// meant 200 threads, every one a task that RLIMIT_NPROC or pids.max
// can make the Go runtime's next pthread_create fail over. Waiting
// must not cost a thread per service.
func TestOnDemandWatchersDoNotHoldThreads(t *testing.T) {
	if testing.Short() {
		t.Skip("starts 200 services")
	}
	const n = 200
	dir := t.TempDir()
	set, _ := newTestSet()
	before := osThreadCount(t)
	svcs := make([]*ProcessService, 0, n)
	defer func() {
		for _, svc := range svcs {
			stopAndWait(t, set, svc)
		}
	}()
	for i := 0; i < n; i++ {
		svc := newOnDemand(t, set, "od-threads-"+strconv.Itoa(i),
			filepath.Join(dir, strconv.Itoa(i)+".sock"), []string{"/bin/sleep", "60"})
		svcs = append(svcs, svc)
		set.StartService(svc)
	}
	for _, svc := range svcs {
		if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
			t.Fatalf("%s: state = %v, want STARTED", svc.Name(), got)
		}
	}
	time.Sleep(300 * time.Millisecond)

	after := osThreadCount(t)
	bound := before + runtime.GOMAXPROCS(0) + 30
	t.Logf("threads: %d before, %d with %d on-demand services listening (bound %d)", before, after, n, bound)
	if after > bound {
		t.Errorf("%d OS threads with %d on-demand services listening (%d before): a thread per watcher",
			after, n, before)
	}

	// And a watcher still sees its client.
	conn, err := net.DialTimeout("unix", filepath.Join(dir, "7.sock"), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for svcs[7].PID() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if svcs[7].PID() == 0 {
		t.Error("first client did not launch the service")
	}
}

// osThreadCount reads this process's OS thread count from /proc.
func osThreadCount(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "Threads:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			return n
		}
	}
	t.Fatal("no Threads: line in /proc/self/status")
	return 0
}
