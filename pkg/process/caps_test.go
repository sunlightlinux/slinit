package process

import "testing"

func TestParseCapabilities(t *testing.T) {
	tests := []struct {
		input   string
		want    []uintptr
		wantErr bool
	}{
		{"cap_net_bind_service", []uintptr{CapNetBindService}, false},
		{"cap_net_admin,cap_sys_admin", []uintptr{CapNetAdmin, CapSysAdmin}, false},
		{"net_bind_service sys_admin", []uintptr{CapNetBindService, CapSysAdmin}, false},
		{"CAP_CHOWN", []uintptr{CapChown}, false},
		{"10", []uintptr{10}, false},
		{"", nil, false},
		{"invalid_cap", nil, true},
	}

	for _, tt := range tests {
		caps, err := ParseCapabilities(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseCapabilities(%q): error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if len(caps) != len(tt.want) {
			t.Errorf("ParseCapabilities(%q): got %d caps, want %d", tt.input, len(caps), len(tt.want))
			continue
		}
		for i, c := range caps {
			if c != tt.want[i] {
				t.Errorf("ParseCapabilities(%q)[%d]: got %d, want %d", tt.input, i, c, tt.want[i])
			}
		}
	}
}

func TestParseSecurebits(t *testing.T) {
	tests := []struct {
		input   string
		want    uint32
		wantErr bool
	}{
		{"noroot", SecbitNoroot, false},
		{"keep-caps noroot", SecbitKeepCaps | SecbitNoroot, false},
		{"no-setuid-fixup no-setuid-fixup-locked", SecbitNoSetuidFixup | SecbitNoSetuidFixupLocked, false},
		{"", 0, false},
		{"invalid-flag", 0, true},
	}

	for _, tt := range tests {
		bits, err := ParseSecurebits(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseSecurebits(%q): error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if bits != tt.want {
			t.Errorf("ParseSecurebits(%q): got %d, want %d", tt.input, bits, tt.want)
		}
	}
}

// A locked pair keeps whatever value it has; unlocked pairs take the
// wanted value, set or clear.
func TestSecurebitsKeepLocked(t *testing.T) {
	for _, c := range []struct {
		name                string
		current, bits, want uint32
	}{
		{"nothing locked: wanted value wins", SecbitKeepCaps, SecbitNoroot, SecbitNoroot},
		{"locked on stays on although not wanted",
			SecbitNoroot | SecbitNorootLocked, SecbitKeepCaps,
			SecbitNoroot | SecbitNorootLocked | SecbitKeepCaps},
		{"locked off stays off although wanted",
			SecbitNorootLocked, SecbitNoroot,
			SecbitNorootLocked},
		{"a lock wanted on an unlocked pair is applied",
			0, SecbitExecRestrictFile | SecbitExecRestrictFileLocked,
			SecbitExecRestrictFile | SecbitExecRestrictFileLocked},
		{"other pairs are unaffected by one lock",
			SecbitNoSetuidFixupLocked, SecbitNoroot | SecbitNoCapAmbientRaise,
			SecbitNoSetuidFixupLocked | SecbitNoroot | SecbitNoCapAmbientRaise},
	} {
		if got := SecurebitsKeepLocked(c.current, c.bits); got != c.want {
			t.Errorf("%s: SecurebitsKeepLocked(%#x, %#x) = %#x, want %#x",
				c.name, c.current, c.bits, got, c.want)
		}
	}
}

func TestSecurebitsStringRoundTrips(t *testing.T) {
	bits := SecbitNoroot | SecbitExecRestrictFile | SecbitExecRestrictFileLocked
	s := SecurebitsString(bits)
	if s != "noroot exec-restrict-file exec-restrict-file-locked" {
		t.Errorf("SecurebitsString = %q", s)
	}
	if back, err := ParseSecurebits(s); err != nil || back != bits {
		t.Errorf("ParseSecurebits(%q) = %#x, %v; want %#x", s, back, err, bits)
	}
	if SecurebitsString(0) != "" {
		t.Errorf("SecurebitsString(0) = %q, want empty", SecurebitsString(0))
	}
}
