package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The control server listens before the boot services are started, so
// requests can come in while main is still wiring the server's hooks.
// Main assigned ShutdownFunc, WallFunc and NologinPath after Start, which
// connection goroutines read unlocked: a data race, a shutdown that
// could be held after the hand-over had drained the slot (ACKed, never
// carried out; see TestShutdownGateHandOverRace), and a `shutdown +N`
// scheduled during boot got neither its wall message nor /run/nologin.
//
// The boot service is a FIFO, so loading it blocks until the script has
// made its requests.
func TestShutdownRequestsDuringBootLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sd"), 0755); err != nil {
		t.Fatal(err)
	}
	r := startNS(t, dir, `
mount -t tmpfs none /run && mount -t tmpfs none /etc || exit 1
mkfifo "$D/sd/boot"
"$B/slinit" -m -d "$D/sd" -p "$D/sock" --syslog-socket "" >"$D/log" 2>&1 &
i=0; while [ ! -S "$D/sock" ] && [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done
"$B/slinitctl" -p "$D/sock" shutdown poweroff +3 >>"$D/trace" 2>&1; echo "schedule rc=$?" >>"$D/trace"
if [ -e /run/nologin ]; then echo nologin=yes; else echo nologin=no; fi >>"$D/trace"
"$B/slinitctl" -p "$D/sock" shutdown -c >>"$D/trace" 2>&1; echo "cancel rc=$?" >>"$D/trace"
"$B/slinitctl" -p "$D/sock" shutdown >>"$D/trace" 2>&1; echo "shutdown rc=$?" >>"$D/trace"
printf 'type = process\ncommand = /bin/sleep 100\n' > "$D/sd/boot"
wait
echo "slinit exited $?" >>"$D/trace"`)
	ended := r.wait(25 * time.Second)
	trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	if !ended || !strings.Contains(string(trace), "slinit exited 0") {
		t.Errorf("the shutdown requested during boot was not carried out (ended=%v)", ended)
	}
	if !strings.Contains(string(trace), "nologin=yes") {
		t.Errorf("a shutdown scheduled during boot did not create /run/nologin")
	}
	if t.Failed() {
		t.Logf("trace:\n%s\nslinit log:\n%s", trace, log)
	}
}
