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
