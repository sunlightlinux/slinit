package service

import (
	"net"
	"strings"
	"testing"
)

// The predicate is named after launchd's KeepAlive{NetworkState} and
// uses its definition verbatim: at least one non-loopback interface
// that is up and has an IPv4 or IPv6 address.
func TestNetworkOnlinePredicateRegistered(t *testing.T) {
	kind, ok := PredicateKindByName("network-online")
	if !ok {
		t.Fatal("network-online is not a known predicate")
	}
	if kind != PredNetworkOnline {
		t.Errorf("got kind %v", kind)
	}
	// String() is what renders a predicate in status and errors; the
	// name must survive into it or an operator sees an opaque number.
	p := Predicate{Kind: PredNetworkOnline}
	if got := p.String(); !strings.Contains(got, "network-online") {
		t.Errorf("String() = %q, want it to name the predicate", got)
	}
}

// Agreement with the host is the only honest assertion available: a
// test cannot dictate whether this machine has an address. So compute
// the answer the same way and require the predicate to match it, which
// still catches an inverted or constant implementation.
func TestNetworkOnlineMatchesTheHost(t *testing.T) {
	expect := false
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot enumerate interfaces: %v", err)
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		if len(addrs) > 0 {
			expect = true
		}
	}

	got, why := checkNetworkOnline("")
	if got != expect {
		t.Errorf("predicate says %v, host says %v (%s)", got, expect, why)
	}
	if !got && why == "" {
		t.Error("a false result must carry a reason")
	}
}

// The negated form must be the exact inverse, which is what makes
// `condition-network-online = no` usable.
func TestNetworkOnlineNegationIsTheInverse(t *testing.T) {
	yes, _ := checkNetworkOnline("yes")
	no, _ := checkNetworkOnline("no")
	if yes == no {
		t.Errorf("yes and no agree (%v); one of them is wrong", yes)
	}
	empty, _ := checkNetworkOnline("")
	if empty != yes {
		t.Errorf("empty param (%v) differs from yes (%v)", empty, yes)
	}
}

func TestNetworkOnlineRejectsGarbageParam(t *testing.T) {
	ok, why := checkNetworkOnline("maybe")
	if ok {
		t.Error("a garbage param must not pass")
	}
	if !strings.Contains(why, "invalid") {
		t.Errorf("reason does not say it is invalid: %q", why)
	}
}

// Loopback alone must not count, or the predicate is constant on every
// machine. Verified through the detector rather than by assertion: if
// the host has only loopback, the answer must be false.
func TestNetworkOnlineIgnoresLoopback(t *testing.T) {
	online, detail := detectNetworkOnline()
	if online && strings.HasPrefix(detail, "lo ") {
		t.Errorf("counted loopback as online: %q", detail)
	}
}
