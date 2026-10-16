// Package transport is the SSRF-safe HTTP client shared by all outbound sinks.
package transport

import (
	"errors"
	"net/netip"
)

// ErrBlockedAddress is returned when a connection target resolves to an address outside the public internet.
var ErrBlockedAddress = errors.New("destination address is not allowed")

// ErrNotHTTPS is returned for any outbound request whose URL scheme is not https.
var ErrNotHTTPS = errors.New("destination URL must use https")

// deniedPrefixes lists ranges that the netip.Addr predicates do not already cover.
// Every entry is a non-public or special-purpose range an operator-supplied URL must never reach.
var deniedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT, includes the VPS's Tailscale range
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, includes the limited broadcast address
	netip.MustParsePrefix("::/96"),           // deprecated IPv4-compatible addresses (::127.0.0.1)
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 well-known prefix
	netip.MustParsePrefix("64:ff9b:1::/48"),  // NAT64 local-use prefix
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001::/32"),       // Teredo tunnelling
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4, embeds an arbitrary IPv4 address
	netip.MustParsePrefix("3fff::/20"),       // documentation (RFC 9637)
	netip.MustParsePrefix("fc00::/7"),        // unique local addresses
	netip.MustParsePrefix("fec0::/10"),       // deprecated site-local addresses
}

// IsPublicAddr reports whether addr is a routable public-internet address that outbound sinks may dial.
// It fails closed: an invalid address is never public.
func IsPublicAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	// Unmap first: an IPv4-mapped IPv6 address such as ::ffff:127.0.0.1 reports IsLoopback as false
	// until it is unmapped, which would silently allow a loopback target.
	addr = addr.Unmap().WithZone("")
	if addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() {
		return false
	}
	for _, prefix := range deniedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
