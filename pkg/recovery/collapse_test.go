package recovery

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// TestCollapseCharToAction covers the key-to-action mapping for
// the post-boot-collapse menu, including the Ctrl-B (recovery svc)
// and Ctrl-D (restart boot) aliases requested for muscle-memory
// alignment with the load-fail menu.
func TestCollapseCharToAction(t *testing.T) {
	cases := []struct {
		in   byte
		want CollapseAction
	}{
		{'r', CollapseReboot},
		{'R', CollapseReboot},
		{'p', CollapsePoweroff},
		{'P', CollapsePoweroff},
		{'s', CollapseRestartBoot},
		{'S', CollapseRestartBoot},
		{0x04, CollapseRestartBoot}, // Ctrl-D → "continue booting"
		{'e', CollapseRecoverySvc},
		{'E', CollapseRecoverySvc},
		{0x02, CollapseRecoverySvc}, // Ctrl-B → "escape hatch"
		{'x', CollapseTimeout},      // unknown key → safest = timeout
		{0x00, CollapseTimeout},
	}
	for _, c := range cases {
		if got := collapseCharToAction(c.in); got != c.want {
			t.Errorf("collapseCharToAction(%#x) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestPresentCollapseIntegrationRestart wires presentCollapse
// end-to-end with mock reader/writer. Confirms `s` produces
// CollapseRestartBoot and that the boxed menu made it to output.
func TestPresentCollapseIntegrationRestart(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		pw.Write([]byte("s"))
		pw.Close()
	}()
	var out bytes.Buffer
	got := presentCollapse(pr, &out, 2*time.Second)
	if got != CollapseRestartBoot {
		t.Fatalf("expected CollapseRestartBoot, got %v", got)
	}
	if !strings.Contains(out.String(), "BOOT COLLAPSE") {
		t.Errorf("menu banner missing from output: %q", out.String())
	}
	if !strings.Contains(out.String(), "restart boot sequence") {
		t.Errorf("choice line missing from output")
	}
}

// TestPresentCollapseCtrlDMapsToRestart — Ctrl-D on a raw-mode
// tty delivers 0x04; on canonical mode it delivers io.EOF which
// readByteWithTimeout translates to a synthetic 0x04. Either way
// the collapse menu maps that to CollapseRestartBoot (the
// "continue booting" analog of the load-fail Ctrl-D). Regression
// guard mirroring TestReadActionWithTimeoutEOFIsRetry.
func TestPresentCollapseCtrlDMapsToRestart(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		pw.Close() // EOF → readByteWithTimeout synthesizes 0x04
	}()
	defer pr.Close()

	var out bytes.Buffer
	got := presentCollapse(pr, &out, 2*time.Second)
	if got != CollapseRestartBoot {
		t.Fatalf("EOF should map to CollapseRestartBoot (Ctrl-D semantic), got %v", got)
	}
}

// TestPresentCollapseCtrlBMapsToRecovery — Ctrl-B is the
// escape-hatch shortcut in both menus; here it must fire the
// recovery service.
func TestPresentCollapseCtrlBMapsToRecovery(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		pw.Write([]byte{0x02}) // Ctrl-B
		pw.Close()
	}()
	var out bytes.Buffer
	got := presentCollapse(pr, &out, 2*time.Second)
	if got != CollapseRecoverySvc {
		t.Fatalf("Ctrl-B should map to CollapseRecoverySvc, got %v", got)
	}
}

// TestPresentCollapseTimesOut fires the safety-net path when
// no input arrives before the deadline.
func TestPresentCollapseTimesOut(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()

	var out bytes.Buffer
	start := time.Now()
	got := presentCollapse(pr, &out, 500*time.Millisecond)
	elapsed := time.Since(start)
	if got != CollapseTimeout {
		t.Fatalf("expected CollapseTimeout, got %v", got)
	}
	if elapsed < 400*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("timeout took %v (expected ~500ms)", elapsed)
	}
}

// TestCollapseActionStringRoundtrip — every CollapseAction value
// must stringify distinctly so log lines are unambiguous.
func TestCollapseActionStringRoundtrip(t *testing.T) {
	seen := map[string]CollapseAction{}
	for _, a := range []CollapseAction{
		CollapseReboot, CollapsePoweroff, CollapseRestartBoot,
		CollapseRecoverySvc, CollapseTimeout,
	} {
		s := a.String()
		if s == "" {
			t.Errorf("CollapseAction(%d) has empty String()", int(a))
		}
		if prev, ok := seen[s]; ok {
			t.Errorf("CollapseActions %v and %v both stringify to %q", prev, a, s)
		}
		seen[s] = a
	}
}
