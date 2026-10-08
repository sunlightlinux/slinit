package service

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
