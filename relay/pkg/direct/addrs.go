package direct

import (
	"net"
	"net/netip"
	"slices"
	"strings"
)

// Which addresses the direct path uses. A host listens only on private
// addresses of its own interfaces, never on a wildcard address (which
// would include a public address): RFC 1918 IPv4 (10/8, 172.16/12,
// 192.168/16), IPv4 link-local (169.254/16), IPv6 unique local (fc00::/7)
// and IPv6 link-local (fe80::/10). Controllers dial only such addresses,
// whatever an offer says.

var privateV4 = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
}

var ula = netip.MustParsePrefix("fc00::/7")

// Eligible reports whether addr is a private or link-local address the
// direct path may use.
func Eligible(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsUnspecified() || addr.IsMulticast() {
		return false
	}
	if addr.Is4() {
		return slices.ContainsFunc(privateV4, func(p netip.Prefix) bool { return p.Contains(addr) })
	}
	return ula.Contains(addr.WithZone("")) || addr.IsLinkLocalUnicast()
}

// skipInterface: tunnels and Apple's peer-to-peer links. A VPN's address
// is reached through the VPN, not the local network; AWDL and friends are
// not where the other Mac is.
func skipInterface(iface net.Interface) bool {
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 {
		return true
	}
	for _, prefix := range []string{"utun", "awdl", "llw", "anpi", "gif", "stf", "ipsec", "ppp", "tun", "tap", "wg"} {
		if strings.HasPrefix(iface.Name, prefix) {
			return true
		}
	}
	return false
}

// LocalAddrs lists this machine's eligible addresses (link-local IPv6
// addresses with their interface as zone), private IPv4 first.
func LocalAddrs() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, iface := range ifaces {
		if skipInterface(iface) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			prefix, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			addr := prefix.Addr().Unmap()
			if !Eligible(addr) {
				continue
			}
			if addr.Is6() && addr.IsLinkLocalUnicast() {
				addr = addr.WithZone(iface.Name)
			}
			out = append(out, addr)
		}
	}
	SortAddrs(out)
	return out
}

// rank orders candidates: RFC 1918 IPv4, unique local IPv6, IPv4
// link-local, IPv6 link-local.
func rank(addr netip.Addr) int {
	switch {
	case addr.Is4() && !addr.IsLinkLocalUnicast():
		return 0
	case addr.Is6() && !addr.IsLinkLocalUnicast():
		return 1
	case addr.Is4():
		return 2
	}
	return 3
}

// SortAddrs sorts addresses by rank, then by value.
func SortAddrs(addrs []netip.Addr) {
	slices.SortStableFunc(addrs, func(a, b netip.Addr) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		return a.Compare(b)
	})
}

// LinkLocalZones are this machine's interfaces with an IPv6 link-local
// address: a host's link-local candidate is tried on each of them.
func LinkLocalZones() []string {
	var zones []string
	for _, addr := range LocalAddrs() {
		if addr.Zone() != "" && !slices.Contains(zones, addr.Zone()) {
			zones = append(zones, addr.Zone())
		}
	}
	return zones
}

// Candidates turns an offer's addresses into dial targets: only eligible
// ones (allow decides about the rest, e.g. loopback in tests), link-local
// IPv6 once per local zone, at most max in the order of the offer's ranks.
func Candidates(addrs []string, allow func(netip.Addr) bool, zones []string, max int) []string {
	var parsed []netip.AddrPort
	for _, a := range addrs {
		ap, err := netip.ParseAddrPort(a)
		if err != nil || ap.Port() == 0 {
			continue
		}
		addr := ap.Addr().Unmap().WithZone("")
		if !Eligible(addr) && (allow == nil || !allow(addr)) {
			continue
		}
		parsed = append(parsed, netip.AddrPortFrom(addr, ap.Port()))
	}
	slices.SortStableFunc(parsed, func(a, b netip.AddrPort) int { return rank(a.Addr()) - rank(b.Addr()) })
	var out []string
	for _, ap := range parsed {
		if ap.Addr().Is6() && ap.Addr().IsLinkLocalUnicast() {
			for _, zone := range zones {
				out = append(out, netip.AddrPortFrom(ap.Addr().WithZone(zone), ap.Port()).String())
			}
			continue
		}
		out = append(out, ap.String())
	}
	if len(out) > max {
		out = out[:max]
	}
	return slices.Compact(out)
}
