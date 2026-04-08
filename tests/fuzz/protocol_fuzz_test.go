package fuzz

import (
	"bytes"
	"encoding/binary"
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

// FuzzDecodeServiceName fuzzes the service name decoder.
func FuzzDecodeServiceName(f *testing.F) {
	f.Add([]byte{5, 0, 'h', 'e', 'l', 'l', 'o'})
	f.Add([]byte{0, 0})
	f.Add([]byte{0xFF, 0xFF})
	f.Add([]byte{})
	f.Add([]byte{3, 0, 'a', 'b'}) // truncated

	f.Fuzz(func(t *testing.T, data []byte) {
		control.DecodeServiceName(data)
	})
}

// FuzzDecodeHandle fuzzes the uint32 handle decoder.
func FuzzDecodeHandle(f *testing.F) {
	f.Add([]byte{1, 0, 0, 0})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	f.Add([]byte{})
	f.Add([]byte{1, 2})

	f.Fuzz(func(t *testing.T, data []byte) {
		control.DecodeHandle(data)
	})
}

// FuzzDecodeServiceStatus fuzzes the 12-byte service status decoder.
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

// FuzzDecodeSetEnv fuzzes the set-env request decoder (handle + KEY=VALUE).
func FuzzDecodeSetEnv(f *testing.F) {
	// handle(4) + "KEY=VALUE"
	buf := make([]byte, 4+9)
	binary.LittleEndian.PutUint32(buf, 1)
	copy(buf[4:], "KEY=VALUE")
	f.Add(buf)

	// handle(4) + "KEY" (unset)
	buf2 := make([]byte, 4+3)
	binary.LittleEndian.PutUint32(buf2, 0)
	copy(buf2[4:], "KEY")
	f.Add(buf2)

	f.Add([]byte{})
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		control.DecodeSetEnv(data)
	})
}

// FuzzDecodeEnvList fuzzes the getallenv reply decoder.
func FuzzDecodeEnvList(f *testing.F) {
	f.Add([]byte("KEY1=val1\x00KEY2=val2\x00"))
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte("=\x00"))
	f.Add([]byte("noequals\x00"))

	f.Fuzz(func(t *testing.T, data []byte) {
		control.DecodeEnvList(data)
	})
}

// FuzzDecodeDepRequest fuzzes the add-dep/rm-dep request decoder.
func FuzzDecodeDepRequest(f *testing.F) {
	buf := make([]byte, 9) // handleFrom(4) + handleTo(4) + depType(1)
	f.Add(buf)
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4})

	f.Fuzz(func(t *testing.T, data []byte) {
		control.DecodeDepRequest(data)
	})
}

// FuzzDecodeBootTime fuzzes the boot timing decoder.
func FuzzDecodeBootTime(f *testing.F) {
	f.Add(make([]byte, 32))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		control.DecodeBootTime(data)
	})
}
