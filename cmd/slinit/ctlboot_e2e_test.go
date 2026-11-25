package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --ready-fd reported the control socket before it existed: the boot
// services were started, and an internal boot service reached STARTED
// and fired the notification, before the server was listening. A
// client acting on the notification at once found no socket.
func TestReadyFDSocketIsListening(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "boot", "type = internal\n")
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	startNS(t, dir, `exec "$B/slinit" -u -d "$D/sd" -p "$D/sock" -F 3 >"$D/log" 2>&1`, wr)
	wr.Close()
	rd.SetReadDeadline(time.Now().Add(15 * time.Second))
	buf := make([]byte, 256)
	n, err := rd.Read(buf)
	if err != nil {
		t.Fatalf("no ready notification: %v", err)
	}
	sock := strings.TrimRight(string(buf[:n]), "\x00")
	if conn, err := net.Dial("unix", sock); err != nil {
		log, _ := os.ReadFile(filepath.Join(dir, "log"))
		t.Errorf("control socket %s not listening when ready was reported: %v\nslinit log:\n%s", sock, err, log)
	} else {
		conn.Close()
	}
}

// options = pass-cs-fd failed for a service started at boot: the hook
// handing the server end to the control server was wired after the boot
// services were started, so the server end was closed and the service's
// first write got EPIPE.
func TestPassCSFDAtBoot(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "boot", "type = internal\ndepends-on: csuser\n")
	writeService(t, dir, "csuser", `
type = process
command = /bin/sh -c "$$B/slinitctl --use-passed-cfd list > `+dir+`/out.tmp 2>&1; echo rc=$$? >> `+dir+`/out.tmp; mv `+dir+`/out.tmp `+dir+`/out; exec sleep 100"
options = pass-cs-fd
`)
	startNS(t, dir, `exec "$B/slinit" -u -d "$D/sd" -p "$D/sock" >"$D/log" 2>&1`)
	out := waitFile(t, filepath.Join(dir, "out"), 15*time.Second)
	if !strings.Contains(out, "rc=0") {
		t.Errorf("slinitctl --use-passed-cfd in a boot service:\n%s", out)
	}
}
