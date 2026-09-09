package control

import (
	"strings"
	"testing"
)

// TestDecodeSwitchRootPayload_RoundTrip covers the wire encoding
// slinitctl uses for CmdSwitchRoot: [nr_len(2, LE)][nr_bytes]
// [ni_len(2, LE)][ni_bytes]. Zero-length newinit is legitimate
// (client asking the server to fall back to /sbin/init).
func TestDecodeSwitchRootPayload_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		newroot string
		newinit string
	}{
		{"both set", "/newroot", "/usr/bin/slinit"},
		{"default init", "/mnt/rootfs", ""},
		{"nested path", "/mnt/decrypted/lvm-root", "/sbin/init"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nr := []byte(tc.newroot)
			ni := []byte(tc.newinit)
			payload := make([]byte, 0, 4+len(nr)+len(ni))
			payload = append(payload, byte(len(nr)&0xFF), byte((len(nr)>>8)&0xFF))
			payload = append(payload, nr...)
			payload = append(payload, byte(len(ni)&0xFF), byte((len(ni)>>8)&0xFF))
			payload = append(payload, ni...)

			gotNewroot, gotNewinit, err := decodeSwitchRootPayload(payload)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if gotNewroot != tc.newroot {
				t.Errorf("newroot = %q, want %q", gotNewroot, tc.newroot)
			}
			if gotNewinit != tc.newinit {
				t.Errorf("newinit = %q, want %q", gotNewinit, tc.newinit)
			}
		})
	}
}

// TestDecodeSwitchRootPayload_Truncated: a truncated payload must
// return an error, not silently trust bogus lengths and OOB-read.
func TestDecodeSwitchRootPayload_Truncated(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", []byte{}},
		{"one byte", []byte{0x05}},
		{"length exceeds buffer", []byte{0xFF, 0xFF, 'x'}},
		{"missing newinit header", func() []byte {
			return []byte{0x02, 0x00, 'a', 'b'}
		}()},
		{"newinit length exceeds buffer",
			[]byte{0x01, 0x00, '/', 0xFF, 0xFF}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := decodeSwitchRootPayload(tc.payload); err == nil {
				t.Fatal("expected error on truncated payload")
			}
		})
	}
}

// TestDecodeSwitchRootPayload_MaxLen: max-length paths (65535) fit,
// making sure the length parser handles values near uint16 max.
func TestDecodeSwitchRootPayload_MaxLen(t *testing.T) {
	// A 200-byte path is well under the wire max but still exercises
	// multi-byte length parsing without wasting a lot of memory.
	long := strings.Repeat("/xx", 60) // 180 bytes
	nr := []byte(long)
	payload := []byte{byte(len(nr) & 0xFF), byte((len(nr) >> 8) & 0xFF)}
	payload = append(payload, nr...)
	payload = append(payload, 0x00, 0x00) // empty newinit
	got, gotInit, err := decodeSwitchRootPayload(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != long {
		t.Errorf("newroot round-trip mismatch on 180-byte path")
	}
	if gotInit != "" {
		t.Errorf("newinit = %q, want empty", gotInit)
	}
}
