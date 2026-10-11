package service

import (
	"net"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestResolveMulticastGroupAcceptsLiterals(t *testing.T) {
	for _, g := range []string{"224.0.0.251", "239.255.42.1", "ff02::fb", "ff05::1:3"} {
		ip, err := resolveMulticastGroup(g)
		if err != nil {
			t.Errorf("resolveMulticastGroup(%q) = %v, want it accepted", g, err)
			continue
		}
		if !ip.IsMulticast() {
			t.Errorf("resolveMulticastGroup(%q) returned non-multicast %v", g, ip)
		}
	}
}

func TestResolveMulticastGroupRejectsUnicast(t *testing.T) {
	// A unicast address is the mistake worth catching by name: both
	// kernels reject the join with a bare EINVAL that says nothing.
	for _, g := range []string{"192.168.1.1", "10.0.0.1", "::1", "2001:db8::1"} {
		if _, err := resolveMulticastGroup(g); err == nil {
			t.Errorf("resolveMulticastGroup(%q) succeeded, want rejection", g)
		} else if !strings.Contains(err.Error(), "not a multicast address") {
			t.Errorf("resolveMulticastGroup(%q) error = %v, want it to name the reason", g, err)
		}
	}
}

func TestJoinMulticastGroupRejectsFamilyMismatch(t *testing.T) {
	// launchd calls this case "undefined". slinit refuses it, so the two
	// directions must both fail, and say which way round the mismatch is.
	cases := []struct {
		network, group, want string
	}{
		{"udp4", "ff02::fb", "IPv6 but the listener is udp4"},
		{"udp6", "224.0.0.251", "IPv4 but the listener is udp6"},
	}
	for _, c := range cases {
		// fd -1 is safe: the family check must reject before any syscall.
		err := joinMulticastGroup(-1, c.network, c.group)
		if err == nil {
			t.Errorf("joinMulticastGroup(%s, %s) succeeded, want mismatch error", c.network, c.group)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("joinMulticastGroup(%s, %s) error = %v, want %q", c.network, c.group, err, c.want)
		}
	}
}

// bindUDP returns a bound datagram fd of the given family, or skips.
func bindUDP(t *testing.T, network, addr string) int {
	t.Helper()
	conn, err := net.ListenPacket(network, addr)
	if err != nil {
		t.Skipf("cannot bind %s %s: %v", network, addr, err)
	}
	t.Cleanup(func() { conn.Close() })
	f, err := conn.(*net.UDPConn).File()
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return int(f.Fd())
}

func TestJoinMulticastGroupActuallyJoinsIPv4(t *testing.T) {
	fd := bindUDP(t, "udp4", "0.0.0.0:0")
	if err := joinMulticastGroup(fd, "udp4", "239.255.42.99"); err != nil {
		t.Fatalf("joinMulticastGroup: %v", err)
	}
	// The kernel is the oracle: a second join of the same group on the
	// same socket returns EADDRINUSE, which is only reachable if the
	// first one took effect. Asserting on a nil error alone would pass
	// against a function that did nothing.
	err := joinMulticastGroup(fd, "udp4", "239.255.42.99")
	if err == nil {
		t.Fatal("re-joining the same group succeeded; the first join cannot have registered")
	}
	if !strings.Contains(err.Error(), unix.EADDRINUSE.Error()) {
		t.Errorf("re-join error = %v, want EADDRINUSE", err)
	}
}

func TestJoinMulticastGroupActuallyJoinsIPv6(t *testing.T) {
	fd := bindUDP(t, "udp6", "[::]:0")
	if err := joinMulticastGroup(fd, "udp6", "ff05::1:99"); err != nil {
		t.Skipf("IPv6 multicast unavailable here: %v", err)
	}
	if err := joinMulticastGroup(fd, "udp6", "ff05::1:99"); err == nil {
		t.Fatal("re-joining the same group succeeded; the first join cannot have registered")
	}
}

func TestSocketMulticastGroupAccessorRoundTrips(t *testing.T) {
	sr := &ServiceRecord{}
	if got := sr.SocketMulticastGroup(); got != "" {
		t.Errorf("default = %q, want empty", got)
	}
	sr.SetSocketMulticastGroup("224.0.0.251")
	if got := sr.SocketMulticastGroup(); got != "224.0.0.251" {
		t.Errorf("after set = %q", got)
	}
}
