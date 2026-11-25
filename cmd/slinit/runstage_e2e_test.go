package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// In container mode slinit is PID 1 of its namespace as well, and took
// that for the machine's: it mounted a fresh tmpfs on /run, hiding the
// runtime's /run/secrets, and pointed its output at /dev/console, so
// the runtime's log stream got nothing.
func TestContainerModeLeavesRunAndStdout(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "boot", `
type = process
command = /bin/sh -c "cat /run/secrets/db_password > `+dir+`/out 2>&1 || echo MISSING > `+dir+`/out"
`)
	r := startNS(t, dir, `
mount -t tmpfs none /run && mkdir /run/secrets && echo s3cr3t > /run/secrets/db_password
: > "$D/console" && mount --bind "$D/console" /dev/console
exec "$B/slinit" -o -d "$D/sd" -p "$D/sock" --syslog-socket ""`)
	if !r.wait(15 * time.Second) {
		t.Fatalf("container did not exit after its boot service did\nstdout:\n%s\nstderr:\n%s", &r.stdout, &r.stderr)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "out"))
	if got := strings.TrimSpace(string(out)); got != "s3cr3t" {
		t.Errorf("boot service read %q from /run/secrets; want s3cr3t", got)
	}
	if !strings.Contains(r.stdout.String(), "boot") {
		t.Errorf("nothing about the boot service on the runtime's stdout:\n%s", &r.stdout)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "console")); len(b) > 0 {
		t.Errorf("container-mode output went to /dev/console:\n%s", b)
	}
}

// As PID 1, a /run that is already a mountpoint — the initramfs's, with
// the udev database and cryptsetup state — is kept by the default
// --run-mode=mount, as its documentation says; a fresh tmpfs was
// mounted over it.
func TestPID1KeepsMountedRun(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "boot", `
type = process
command = /bin/sh -c "cat /run/udev/marker > `+dir+`/out 2>&1 || echo MISSING > `+dir+`/out; exec sleep 100"
`)
	startNS(t, dir, `
mount -t tmpfs none /run && mkdir /run/udev && echo initramfs-state > /run/udev/marker
exec "$B/slinit" -d "$D/sd" -p "$D/sock" --syslog-socket "" --no-watchdog`)
	if got := strings.TrimSpace(waitFile(t, filepath.Join(dir, "out"), 15*time.Second)); got != "initramfs-state" {
		t.Errorf("boot service read %q from the initramfs's /run; want initramfs-state", got)
	}
}

// As PID 1, slinit mounted proc on /proc without looking, stacking a
// new procfs on the one there (and another per soft reboot), so the
// hidepid set on the first stopped applying to anything.
func TestPID1KeepsMountedProc(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "boot", `
type = process
command = /bin/sh -c "grep -c ' /proc ' /proc/self/mountinfo > `+dir+`/out.tmp; grep ' /proc ' /proc/self/mountinfo | tail -n 1 >> `+dir+`/out.tmp; mv `+dir+`/out.tmp `+dir+`/out; exec sleep 100"
`)
	startNS(t, dir, `
mount -o remount,hidepid=invisible /proc
grep -c ' /proc ' /proc/self/mountinfo > "$D/before"
exec "$B/slinit" -d "$D/sd" -p "$D/sock" --syslog-socket "" --no-watchdog`)
	out := waitFile(t, filepath.Join(dir, "out"), 15*time.Second)
	before, _ := os.ReadFile(filepath.Join(dir, "before"))
	lines := strings.SplitN(out, "\n", 2)
	if len(lines) < 2 || lines[0] != strings.TrimSpace(string(before)) || !strings.Contains(lines[1], "hidepid=invisible") {
		t.Errorf("/proc mounts before slinit: %s; seen by a service:\n%s", strings.TrimSpace(string(before)), out)
	}
}
