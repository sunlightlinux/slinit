package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRlimitMemlockParsed(t *testing.T) {
	for _, c := range []struct {
		in   string
		want [2]uint64
	}{
		{"unlimited", [2]uint64{^uint64(0), ^uint64(0)}},
		{"65536", [2]uint64{65536, 65536}},
		{"65536:unlimited", [2]uint64{65536, ^uint64(0)}},
	} {
		desc, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\nrlimit-memlock = "+c.in+"\n"), "m", "m")
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if desc.RlimitMemlock == nil || *desc.RlimitMemlock != c.want {
			t.Errorf("%q: got %v, want %v", c.in, desc.RlimitMemlock, c.want)
		}
	}
	if _, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\nrlimit-memlock = lots\n"), "m", "m"); err == nil {
		t.Error("rlimit-memlock = lots was accepted")
	}
}

func TestSystemdRlimitValues(t *testing.T) {
	for _, c := range []struct {
		in   string
		size bool
		want string
	}{
		{"infinity", false, "unlimited"},
		{"4096", false, "4096"},
		{"1024:infinity", false, "1024:unlimited"},
		{"64M", true, "67108864"},
		{"1G:infinity", true, "1073741824:unlimited"},
		{"512K", true, "524288"},
		{"8B", true, "8"},
		// NOFILE is a count in systemd, not a size: no suffixes there.
		{"4K", false, "4K"},
		// Not systemd syntax slinit knows: left for the parser to reject.
		{"1.5G", true, "1.5G"},
	} {
		if got := systemdRlimit(c.in, c.size); got != c.want {
			t.Errorf("systemdRlimit(%q, %v) = %q, want %q", c.in, c.size, got, c.want)
		}
	}
}

// A unit with LimitNOFILE=infinity — among the most common lines a unit
// has — used to fail to load at all: the value was copied through, and
// slinit's own syntax for it is "unlimited".
func TestSystemdUnitWithInfiniteLimitsLoads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dpdk.service")
	unit := "[Service]\nExecStart=/bin/true\nLimitNOFILE=infinity\nLimitMEMLOCK=infinity\nLimitCORE=64M:infinity\n"
	if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	desc, err := SystemdUnitToServiceDescription(p, "dpdk")
	if err != nil {
		t.Fatalf("unit with infinite limits did not load: %v", err)
	}
	inf := ^uint64(0)
	if desc.RlimitNofile == nil || *desc.RlimitNofile != [2]uint64{inf, inf} {
		t.Errorf("nofile %v", desc.RlimitNofile)
	}
	if desc.RlimitMemlock == nil || *desc.RlimitMemlock != [2]uint64{inf, inf} {
		t.Errorf("memlock %v", desc.RlimitMemlock)
	}
	if desc.RlimitCore == nil || *desc.RlimitCore != [2]uint64{64 << 20, inf} {
		t.Errorf("core %v", desc.RlimitCore)
	}
}
