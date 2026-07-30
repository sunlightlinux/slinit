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
		})
	}
}
