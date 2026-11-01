package main

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// execStageName is a hand copy of process.ExecStage.String(), kept here so
// the CLI does not import pkg/process for one table. A stage added on the
// daemon side and not here would print as "unknown" in `slinitctl status`
// -- the drift dinit guarded against in e518520. This fails as soon as
// the two disagree.
func TestExecStageNameMatchesProcess(t *testing.T) {
	n := 0
	for ; ; n++ {
		want := process.ExecStage(n).String()
		if strings.HasPrefix(want, "ExecStage(") {
			break // past the last stage pkg/process knows
		}
		if got := execStageName(uint16(n)); got != want {
			t.Errorf("stage %d: slinitctl says %q, pkg/process says %q", n, got, want)
		}
	}
	if n == 0 {
		t.Fatal("pkg/process reported no stages at all")
	}

	// One past the last stage: both sides must agree it is unknown, so the
	// CLI has no stage pkg/process lacks.
	if got := execStageName(uint16(n)); got != "unknown" {
		t.Errorf("stage %d is past pkg/process's last stage, but slinitctl names it %q", n, got)
	}
}
