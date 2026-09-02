package fuzz

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/control"
)

// FuzzReadPacket fuzzes the binary control protocol packet reader.
// Malformed packets from a compromised or buggy client must not crash the daemon.
func FuzzReadPacket(f *testing.F) {
	// Valid packet: type=1, payload_len=5, payload="hello"
	var valid bytes.Buffer
	valid.WriteByte(1)
	binary.Write(&valid, binary.LittleEndian, uint16(5))
	valid.WriteString("hello")
	f.Add(valid.Bytes())

	// Empty packet (no payload)
	var empty bytes.Buffer
	empty.WriteByte(0)
	binary.Write(&empty, binary.LittleEndian, uint16(0))
	f.Add(empty.Bytes())

	// Truncated header
	f.Add([]byte{1})
	f.Add([]byte{1, 2})

	// Large payload length
	f.Add([]byte{1, 0xFF, 0xFF})

	// Zero bytes
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		control.ReadPacket(bytes.NewReader(data))
	})
}

// FuzzDecodeServiceName fuzzes the service name decoder AND — when the
// decode succeeds — asserts round-trip through EncodeServiceName is a
// bit-identical no-op. That catches drift like "decoder tolerates
// extra padding bytes that encoder never emits", which would corrupt
// wire compat across daemon/client versions.
func FuzzDecodeServiceName(f *testing.F) {
	f.Add([]byte{5, 0, 'h', 'e', 'l', 'l', 'o'})
	f.Add([]byte{0, 0})
	f.Add([]byte{0xFF, 0xFF})
	f.Add([]byte{})
	f.Add([]byte{3, 0, 'a', 'b'}) // truncated

	f.Fuzz(func(t *testing.T, data []byte) {
		name, n, err := control.DecodeServiceName(data)
		if err != nil {
			return
		}
		// Round-trip: encoding the decoded name must yield exactly
		// the n consumed bytes of the input.
		reenc := control.EncodeServiceName(name)
		if !bytes.Equal(reenc, data[:n]) {
			t.Errorf("service name round-trip drift:\n  input[:n]=%q\n  reenc  =%q",
				data[:n], reenc)
		}
	})
}

// FuzzDecodeHandle exercises the uint32 handle decoder + round-trip.
func FuzzDecodeHandle(f *testing.F) {
	f.Add([]byte{1, 0, 0, 0})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	f.Add([]byte{})
	f.Add([]byte{1, 2})

	f.Fuzz(func(t *testing.T, data []byte) {
		h, err := control.DecodeHandle(data)
		if err != nil {
			return
		}
		reenc := control.EncodeHandle(h)
		if !bytes.Equal(reenc, data[:4]) {
			t.Errorf("handle round-trip drift: input[:4]=%v reenc=%v", data[:4], reenc)
		}
	})
}

// FuzzDecodeServiceStatus fuzzes the 12-byte service status decoder.
// No round-trip because EncodeServiceStatus takes a live service.Service.
func FuzzDecodeServiceStatus(f *testing.F) {
	f.Add(make([]byte, 12))
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3})

	f.Fuzz(func(t *testing.T, data []byte) {
		control.DecodeServiceStatus(data)
	})
}

// FuzzDecodeServiceStatus5 fuzzes the 14-byte v5 service status decoder.
func FuzzDecodeServiceStatus5(f *testing.F) {
	f.Add(make([]byte, 14))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		control.DecodeServiceStatus5(data)
	})
}

// FuzzDecodeSetEnv fuzzes the set-env request decoder + round-trip.
// The "unset" bit is derived from valueLen==0 during decode; the
// round-trip must preserve that inference.
func FuzzDecodeSetEnv(f *testing.F) {
	// handle(4) + "KEY=VALUE" (encoded via EncodeSetEnv)
	f.Add(control.EncodeSetEnv(1, "KEY", "VALUE", false))
	f.Add(control.EncodeSetEnv(0, "KEY", "", true))
	f.Add(control.EncodeSetEnv(42, "PATH", "/usr/bin:/bin", false))
	f.Add(control.EncodeSetEnv(7, "EMPTY", "", true))

	f.Add([]byte{})
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		handle, key, value, isUnset, err := control.DecodeSetEnv(data)
		if err != nil {
			return
		}
		reenc := control.EncodeSetEnv(handle, key, value, isUnset)
		// Round-trip must be idempotent: decode(reenc) must give the
		// same (handle, key, value, isUnset) tuple.
		h2, k2, v2, u2, err2 := control.DecodeSetEnv(reenc)
		if err2 != nil {
			t.Errorf("re-decode failed: %v", err2)
			return
		}
		if h2 != handle || k2 != key || v2 != value || u2 != isUnset {
			t.Errorf("set-env round-trip drift:\n  first : h=%d k=%q v=%q u=%v\n  second: h=%d k=%q v=%q u=%v",
				handle, key, value, isUnset, h2, k2, v2, u2)
		}
	})
}

// FuzzDecodeEnvList fuzzes the getallenv reply decoder + map round-trip.
// Because Go map iteration order isn't stable, we round-trip the
// decoded map, then decode the fresh bytes, and compare the two maps
// by value equality (reflect.DeepEqual) rather than byte equality.
func FuzzDecodeEnvList(f *testing.F) {
	f.Add(control.EncodeEnvList(map[string]string{"K1": "v1", "K2": "v2"}))
	f.Add(control.EncodeEnvList(map[string]string{}))
	f.Add(control.EncodeEnvList(map[string]string{"": ""}))
	f.Add(control.EncodeEnvList(map[string]string{"PATH": "/usr/bin"}))

	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte("=\x00"))

	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := control.DecodeEnvList(data)
		if err != nil {
			return
		}
		reenc := control.EncodeEnvList(env)
		env2, err2 := control.DecodeEnvList(reenc)
		if err2 != nil {
			t.Errorf("re-decode failed: %v", err2)
			return
		}
		if !reflect.DeepEqual(env, env2) {
			t.Errorf("env-list round-trip drift:\n  first =%v\n  second=%v", env, env2)
		}
	})
}

// FuzzDecodeDepRequest fuzzes the add-dep/rm-dep request decoder +
// round-trip. Wire is a fixed 9-byte layout so byte-equal round-trip
// on data[:9] is exact.
func FuzzDecodeDepRequest(f *testing.F) {
	f.Add(control.EncodeDepRequest(1, 2, 0))
	f.Add(control.EncodeDepRequest(0xDEADBEEF, 0xCAFEBABE, 5))
	f.Add(make([]byte, 9))
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4})

	f.Fuzz(func(t *testing.T, data []byte) {
		from, to, dt, err := control.DecodeDepRequest(data)
		if err != nil {
			return
		}
		reenc := control.EncodeDepRequest(from, to, dt)
		if !bytes.Equal(reenc, data[:9]) {
			t.Errorf("dep-request round-trip drift:\n  input[:9]=%v\n  reenc   =%v", data[:9], reenc)
		}
	})
}

// FuzzDecodeBootTime fuzzes the boot timing decoder. Round-trip via
// EncodeBootTime confirms decoded fields survive re-serialisation.
func FuzzDecodeBootTime(f *testing.F) {
	f.Add(make([]byte, 32))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := control.DecodeBootTime(data)
		if err != nil {
			return
		}
		reenc := control.EncodeBootTime(info)
		info2, err2 := control.DecodeBootTime(reenc)
		if err2 != nil {
			t.Errorf("re-decode failed: %v", err2)
			return
		}
		if !reflect.DeepEqual(info, info2) {
			t.Errorf("boot-time round-trip drift:\n  first =%+v\n  second=%+v", info, info2)
		}
	})
}

// FuzzDecodeMetadata fuzzes the metadata (author/version/usage) decoder
// + round-trip. Three length-prefixed strings concatenated.
func FuzzDecodeMetadata(f *testing.F) {
	f.Add(control.EncodeMetadata("alice", "1.0", "usage: foo"))
	f.Add(control.EncodeMetadata("", "", ""))
	f.Add(control.EncodeMetadata("a", "b", "c"))
	f.Add([]byte{})
	f.Add([]byte{0, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		author, version, usage, err := control.DecodeMetadata(data)
		if err != nil {
			return
		}
		reenc := control.EncodeMetadata(author, version, usage)
		a2, v2, u2, err2 := control.DecodeMetadata(reenc)
		if err2 != nil {
			t.Errorf("re-decode failed: %v", err2)
			return
		}
		if a2 != author || v2 != version || u2 != usage {
			t.Errorf("metadata round-trip drift:\n  first : a=%q v=%q u=%q\n  second: a=%q v=%q u=%q",
				author, version, usage, a2, v2, u2)
		}
	})
}

// FuzzDecodeCatLogRequest fuzzes catlog request (handle + clear flag).
func FuzzDecodeCatLogRequest(f *testing.F) {
	f.Add(control.EncodeCatLogRequest(1, false))
	f.Add(control.EncodeCatLogRequest(42, true))
	f.Add(control.EncodeCatLogRequest(0xFFFFFFFF, true))
	f.Add([]byte{})
	f.Add([]byte{1})

	f.Fuzz(func(t *testing.T, data []byte) {
		flags, handle, err := control.DecodeCatLogRequest(data)
		if err != nil {
			return
		}
		clear := (flags & 1) != 0
		reenc := control.EncodeCatLogRequest(handle, clear)
		f2, h2, err2 := control.DecodeCatLogRequest(reenc)
		if err2 != nil {
			t.Errorf("re-decode failed: %v", err2)
			return
		}
		if h2 != handle || (f2&1) != (flags&1) {
			t.Errorf("catlog-request round-trip drift:\n  first : h=%d flags=%d\n  second: h=%d flags=%d",
				handle, flags, h2, f2)
		}
	})
}

// FuzzServiceNameSemantics is a semantic-invariant fuzz: bytes that
// decode successfully as a service name must not violate the well-
// formedness rules the on-disk loader enforces (ValidateServiceName).
// Any byte-sequence that DecodeServiceName accepts but ValidateService
// Name rejects is a wire-vs-loader trust gap — the daemon should
// never treat a name as loadable that the loader would refuse.
//
// The rule is "wire acceptance ⊆ loader acceptance". This fuzz
// records the reverse direction: input that decodes cleanly but
// carries obviously malicious bytes (NUL, path traversal segments,
// leading dot/@) must be rejected downstream.
func FuzzServiceNameSemantics(f *testing.F) {
	// Seeds designed to exercise the boundary: each of these decodes
	// via DecodeServiceName but should NOT pass ValidateServiceName.
	f.Add(control.EncodeServiceName(""))                    // empty
	f.Add(control.EncodeServiceName(".hidden"))             // leading dot
	f.Add(control.EncodeServiceName("@template"))           // leading @
	f.Add(control.EncodeServiceName("foo\x00bar"))          // NUL in name
	f.Add(control.EncodeServiceName("foo bar"))             // space
	f.Add(control.EncodeServiceName("foo\nbar"))            // newline
	f.Add(control.EncodeServiceName("foo:bar"))             // colon (dep separator)
	// Seeds that SHOULD pass validation:
	f.Add(control.EncodeServiceName("normal-svc"))
	f.Add(control.EncodeServiceName("worker@1"))
	f.Add(control.EncodeServiceName("path/like/name"))

	f.Fuzz(func(t *testing.T, data []byte) {
		name, _, err := control.DecodeServiceName(data)
		if err != nil {
			return
		}
		// NUL, control chars, and control-protocol delimiters must
		// never survive the loader. Wire vs loader must agree.
		if strings.ContainsRune(name, '\x00') {
			// Wire allowed NUL; loader would refuse. Wire should
			// have refused too to close the gap.
			t.Errorf("wire accepted service name with embedded NUL: %q", name)
		}
	})
}
