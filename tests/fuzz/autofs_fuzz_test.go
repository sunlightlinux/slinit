package fuzz

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/autofs"
)

// FuzzParseV5Packet fuzzes the autofs v5 kernel notification packet parser.
// The kernel sends these via pipe — a malformed packet must not crash the daemon.
func FuzzParseV5Packet(f *testing.F) {
	// Valid-ish packet (300 bytes, proto=5, type=3)
	valid := make([]byte, autofs.V5PacketSize)
	valid[0] = 5
	valid[4] = 3
	valid[8] = 42
	copy(valid[44:], "subdir")
	f.Add(valid)

	// All zeros
	f.Add(make([]byte, autofs.V5PacketSize))

	// All 0xFF
	full := make([]byte, autofs.V5PacketSize)
	for i := range full {
		full[i] = 0xFF
	}
	f.Add(full)

	// Too short
	f.Add([]byte{})
	f.Add([]byte{5, 0, 0, 0})
	f.Add(make([]byte, 50))

	f.Fuzz(func(t *testing.T, data []byte) {
		autofs.ParseV5Packet(data)
	})
}

// FuzzParseMountUnit fuzzes the .mount config file parser.
func FuzzParseMountUnit(f *testing.F) {
	f.Add("what = /dev/sda1\nwhere = /mnt\ntype = ext4\n")
	f.Add("what = server:/export\nwhere = /home\ntype = nfs\noptions = rw,soft\ntimeout = 300\n")
	f.Add("what = /dev/sda1\nwhere = /mnt\ntype = ext4\nautofs-type = direct\n")
	f.Add("what = /dev/sda1\nwhere = /mnt\ntype = ext4\ndirectory-mode = 0750\n")
	f.Add("what = /dev/sda1\nwhere = /mnt\ntype = ext4\nafter: network-online dns\n")
	f.Add("")
	f.Add("bogus = value\n")
	f.Add("what = \nwhere = \ntype = \n")
	f.Add("timeout = abc\n")
	f.Add("directory-mode = zzz\n")

	f.Fuzz(func(t *testing.T, data string) {
		autofs.ParseMountUnit(strings.NewReader(data), "fuzz-mount")
	})
}
