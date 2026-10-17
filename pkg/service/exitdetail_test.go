package service

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// The whole point of exitDetail is that an operator never has to decode
// a packed wait status by hand. This pins the three cases, including
// the one that produced the report: nginx's stop-command exited 1 and
// the log said "status 256".
func TestExitDetailNeverPrintsAPackedStatus(t *testing.T) {
	cases := []struct {
		name   string
		status syscall.WaitStatus
		want   string
		// absent is what the raw %v form would have shown.
		absent string
	}{
		{"exit 1 (the reported case)", syscall.WaitStatus(1 << 8), "exit code 1", "256"},
		{"exit 5", syscall.WaitStatus(5 << 8), "exit code 5", "1280"},
		{"exit 0", syscall.WaitStatus(0), "exit code 0", ""},
		{"killed by SIGKILL", syscall.WaitStatus(syscall.SIGKILL), "killed by signal 9", ""},
	}
	for _, c := range cases {
		got := exitDetail(process.ChildExit{Status: c.status})
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: exitDetail = %q, want it to contain %q", c.name, got, c.want)
		}
		if c.absent != "" && strings.Contains(got, c.absent) {
			t.Errorf("%s: exitDetail = %q, still shows the packed value %q",
				c.name, got, c.absent)
		}
	}
}

func TestExitDetailFallsBackForAnUndecodableStatus(t *testing.T) {
	// Neither exited nor signalled — stopped/continued. Saying so beats
	// claiming an exit code that was never set.
	got := exitDetail(process.ChildExit{Status: syscall.WaitStatus(0x7f)})
	if !strings.Contains(got, "raw wait status") {
		t.Errorf("exitDetail = %q, want the raw-status fallback named as such", got)
	}
}

// Signals must never reach a log line through %v. `%v` on a
// syscall.Signal gives the strerror description, which is how a real
// softreboot came to log "stop timeout exceeded, sending killed".
// signalName gives the bare form the config uses, so the operator reads
// the same word in their service file and in the log.
func TestSignalNameMatchesTheConfigVocabulary(t *testing.T) {
	cases := map[syscall.Signal]string{
		syscall.SIGTERM: "TERM",
		syscall.SIGKILL: "KILL",
		syscall.SIGHUP:  "HUP",
		syscall.SIGUSR1: "USR1",
	}
	for sig, want := range cases {
		if got := signalName(sig); got != want {
			t.Errorf("signalName(%d) = %q, want %q", int(sig), got, want)
		}
		// And it must not be the description %v would have produced.
		if got := signalName(sig); got == sig.String() {
			t.Errorf("signalName(%d) returned the %%v description %q", int(sig), got)
		}
	}
}

// TestNoSignalReachesALogLineViaPercentV is the guard that keeps this
// from coming back: it reads the sources and fails on an operator-facing
// message that formats a signal with %v.
func TestNoSignalReachesALogLineViaPercentV(t *testing.T) {
	// Every file in the package, not a hand-listed three: the miss this
	// guards against was a file not on the list.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			files = append(files, n)
		}
	}
	if len(files) < 10 {
		t.Fatalf("only %d sources found; the sweep is not looking where it should", len(files))
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "logger.") {
				continue
			}
			// "sending %v" / "signal %v" are the shapes that produced
			// the report. A signal rendered any other way is fine.
			if strings.Contains(line, "sending %v") || strings.Contains(line, "signal %v") {
				t.Errorf("%s:%d formats a signal with %%v: %s",
					f, i+1, strings.TrimSpace(line))
			}
			// And the packed wait status. This half is here because
			// fixing the type = process path first left the bgprocess
			// one — which is where the reported line actually came
			// from, since a forking daemon's stop goes through there.
			// One file looking right is not the file being right.
			if strings.Contains(line, "status %v") || strings.Contains(line, "with status %d") {
				t.Errorf("%s:%d formats a wait status raw: %s",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
