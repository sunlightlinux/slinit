package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The final sweep SIGKILLs every process, and the event loop that reaped
// them has returned by then. Nothing collected the killed: each stayed a
// zombie holding a task slot. With the system at its task limit (here a
// respawning loop filling RLIMIT_NPROC once slinit is up, as a fork bomb
// would) the shutdown hook's fork then failed with EAGAIN and the hook
// never ran.
func TestShutdownHookRunsAfterSweepAtTaskLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sd"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sd", "boot"),
		[]byte("type = process\ncommand = /bin/sleep 1000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r := startNS(t, dir, `
mount -t tmpfs none /run && mount -t tmpfs none /etc || exit 1
mkdir /etc/slinit
printf '#!/bin/sh\necho "$1" >"$D/hook"\nexit 1\n' >/etc/slinit/shutdown-hook
chmod 755 /etc/slinit/shutdown-hook
ulimit -u 150
( sleep 1; trap '' TERM; while :; do sleep 1000 & done ) 2>/dev/null &
exec "$B/slinit" -d "$D/sd" -p "$D/sock" --syslog-socket "" --no-watchdog --log-file "$D/log" -B
`)
	sock := filepath.Join(dir, "sock")
	ctl := filepath.Join(nsBinDir(t), "slinitctl")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command(ctl, "-p", sock, "is-started", "boot").Run() == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond) // past slinit's start-up, the loop reaches the limit
	if err := exec.Command(ctl, "-p", sock, "poweroff").Run(); err != nil {
		log, _ := os.ReadFile(filepath.Join(dir, "log"))
		t.Fatalf("slinitctl poweroff: %v\nlog:\n%s", err, log)
	}
	ended := r.wait(20 * time.Second)
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	hook, _ := os.ReadFile(filepath.Join(dir, "hook"))
	if !ended {
		t.Fatalf("PID 1 still up 20s after poweroff\nlog:\n%s", log)
	}
	if strings.TrimSpace(string(hook)) != "poweroff" {
		t.Errorf("the shutdown hook did not run after the sweep (hook=%q)\nlog:\n%s", hook, log)
	}
}
