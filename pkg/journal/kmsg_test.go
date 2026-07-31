package journal

import "testing"

func TestParseKmsgLine(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantMsg  string
		wantPri  Priority
		wantTr   Transport
		wantNil  bool
	}{
		{
			name:    "typical kernel err",
			in:      "3,42,1234567,-;kernel error message",
			wantMsg: "kernel error message",
			wantPri: PriorityError,
			wantTr:  TransportKernel,
		},
		{
			name:    "kern info with facility bits (fac=0<<3 | sev=6)",
			in:      "6,100,9999999,-;random info from kernel",
			wantMsg: "random info from kernel",
			wantPri: PriorityInfo,
			wantTr:  TransportKernel,
		},
		{
			name:    "high PRI with facility encoded (fac=3<<3 | sev=4 = 28)",
			in:      "28,5,1000000,-;daemon warning via kernel",
			wantMsg: "daemon warning via kernel",
			wantPri: PriorityWarning,
			wantTr:  TransportKernel,
		},
		{
			name:    "message with embedded semicolons",
			in:      "6,1,1,-;msg with ; in body; keeps rest",
			wantMsg: "msg with ; in body; keeps rest",
			wantPri: PriorityInfo,
			wantTr:  TransportKernel,
		},
		{
			name:    "continuation line (starts with space)",
			in:      " continuation payload",
			wantMsg: "continuation payload",
			wantPri: PriorityInfo,
			wantTr:  TransportKernel,
		},
		{
			name:    "continuation line (starts with tab)",
			in:      "\tanother continuation",
			wantMsg: "another continuation",
			wantPri: PriorityInfo,
			wantTr:  TransportKernel,
		},
		{
			name:    "empty message OK",
			in:      "6,1,1,-;",
			wantMsg: "",
			wantPri: PriorityInfo,
			wantTr:  TransportKernel,
		},
		// Failure cases — return nil.
		{name: "no semicolon", in: "6,1,1,-no-body", wantNil: true},
		{name: "empty line", in: "", wantNil: true},
		{name: "too few header fields", in: "6,1;msg", wantNil: true},
		{name: "non-numeric priority", in: "abc,1,1,-;msg", wantNil: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseKmsgLine(c.in)
			if c.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected event, got nil")
			}
			if got.Msg != c.wantMsg {
				t.Errorf("msg: got %q, want %q", got.Msg, c.wantMsg)
			}
			if got.Prio != c.wantPri {
				t.Errorf("prio: got %v, want %v", got.Prio, c.wantPri)
			}
			if got.Transport != c.wantTr {
				t.Errorf("transport: got %v, want %v", got.Transport, c.wantTr)
			}
			// Every kernel event must self-identify as unit "kernel"
			// so short-format renderers don't fall through to
			// "unknown" and slinit-journalctl -u kernel filters cleanly.
			if got.Unit != "kernel" {
				t.Errorf("unit: got %q, want \"kernel\"", got.Unit)
			}
		})
	}
}

// TestEmitKernelTransportSkipsPID verifies the stampTrusted special
// case for kernel-origin events: no userspace PID/UID/GID leaks onto
// them (they didn't come from a userspace process), while boot ID /
// machine ID / hostname stay populated (per-boot metadata is still
// meaningful for the reader).
func TestEmitKernelTransportSkipsPID(t *testing.T) {
	InitIDs("testhost")
	buf := NewEventBuffer(4)
	e := NewEmitter(buf, "/dev/null-nope")
	defer e.Close()

	evt := &Event{
		Msg:       "fake kernel message",
		Prio:      PriorityWarning,
		Transport: TransportKernel,
		Unit:      "kernel",
	}
	_ = e.Emit(evt)

	if evt.Pid != 0 {
		t.Errorf("kernel event should have _pid=0, got %d", evt.Pid)
	}
	if evt.Uid != 0 {
		t.Errorf("kernel event should have _uid=0, got %d", evt.Uid)
	}
	if evt.Gid != 0 {
		t.Errorf("kernel event should have _gid=0, got %d", evt.Gid)
	}
	// Per-boot/host metadata still stamped.
	if evt.BootID == "" {
		t.Error("kernel event should still carry _boot_id")
	}
	if evt.MachineID == "" {
		t.Error("kernel event should still carry _machine_id")
	}
	if evt.Hostname == "" {
		t.Error("kernel event should still carry _hostname")
	}
}

// TestEmitDriverTransportStampsPID is the flip side: a non-kernel
// event (driver, stdout, native) MUST get the emitting process's
// PID stamped so operators can trace the source.
func TestEmitDriverTransportStampsPID(t *testing.T) {
	InitIDs("testhost")
	buf := NewEventBuffer(4)
	e := NewEmitter(buf, "/dev/null-nope")
	defer e.Close()

	evt := &Event{Msg: "regular event"}
	_ = e.Emit(evt)

	if evt.Pid == 0 {
		t.Error("non-kernel event should have _pid stamped, got 0")
	}
	if evt.Transport != TransportDriver {
		t.Errorf("empty transport should default to driver, got %q", evt.Transport)
	}
}
