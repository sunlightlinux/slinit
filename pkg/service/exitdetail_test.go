package service

import (
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
