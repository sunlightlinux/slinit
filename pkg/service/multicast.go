package service

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// joinMulticastGroup adds fd to the multicast group named by grp, which is
// either a literal address or a hostname. This is launchd's MulticastGroup
// key for a datagram socket.
//
// launchd documents that with a literal address the socket family must also
// be set, "otherwise the results are undefined". slinit takes the family
// from the listener prefix (udp4:/udp6:/udp:) and treats a mismatch as a
// load error instead, because a silently unjoined socket looks exactly like
// a working one: it binds, it starts, and it simply never receives.
//
// The interface is left to the kernel (INADDR_ANY / index 0), which picks it
// from the routing table. Pinning it would need a second directive and a
// reason to want one.
func joinMulticastGroup(fd int, network, grp string) error {
	ip, err := resolveMulticastGroup(grp)
	if err != nil {
		return err
	}

	v4 := ip.To4() != nil
	switch network {
	case "udp4":
		if !v4 {
			return fmt.Errorf("socket-multicast-group: %s is IPv6 but the listener is udp4", grp)
		}
	case "udp6":
		if v4 {
			return fmt.Errorf("socket-multicast-group: %s is IPv4 but the listener is udp6", grp)
		}
	}

	if v4 {
		mreq := &unix.IPMreq{}
		copy(mreq.Multiaddr[:], ip.To4())
		if err := unix.SetsockoptIPMreq(fd, unix.IPPROTO_IP, unix.IP_ADD_MEMBERSHIP, mreq); err != nil {
			return fmt.Errorf("socket-multicast-group %s: IP_ADD_MEMBERSHIP: %w", grp, err)
		}
		return nil
	}

	mreq := &unix.IPv6Mreq{}
	copy(mreq.Multiaddr[:], ip.To16())
	if err := unix.SetsockoptIPv6Mreq(fd, unix.IPPROTO_IPV6, unix.IPV6_JOIN_GROUP, mreq); err != nil {
		return fmt.Errorf("socket-multicast-group %s: IPV6_JOIN_GROUP: %w", grp, err)
	}
	return nil
}

// resolveMulticastGroup turns grp into an IP, resolving a hostname the way
// launchd's getaddrinfo does, and rejects anything that is not a multicast
// address. A unicast address here would be accepted by neither kernel, but
// the error it returns (EINVAL) does not say why, so name it ourselves.
func resolveMulticastGroup(grp string) (net.IP, error) {
	if ip := net.ParseIP(grp); ip != nil {
		if !ip.IsMulticast() {
			return nil, fmt.Errorf("socket-multicast-group: %s is not a multicast address", grp)
		}
		return ip, nil
	}

	addrs, err := net.LookupIP(grp)
	if err != nil {
		return nil, fmt.Errorf("socket-multicast-group: resolving %s: %w", grp, err)
	}
	for _, ip := range addrs {
		if ip.IsMulticast() {
			return ip, nil
		}
	}
	return nil, fmt.Errorf("socket-multicast-group: %s resolved to no multicast address", grp)
}
