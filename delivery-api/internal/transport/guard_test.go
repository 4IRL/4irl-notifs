package transport

import (
	"net/netip"
	"testing"
)

func TestIsPublicAddr(testInstance *testing.T) {
	cases := []struct {
		name    string
		address string
		want    bool
	}{
		{"loopback v4", "127.0.0.1", false},
		{"loopback v4 high", "127.255.255.254", false},
		{"private 10/8", "10.1.2.3", false},
		{"private 172.16/12", "172.16.0.1", false},
		{"private 172.31", "172.31.255.255", false},
		{"private 192.168/16", "192.168.1.1", false},
		{"cloud metadata", "169.254.169.254", false},
		{"link-local v4", "169.254.1.1", false},
		{"cgnat tailscale", "100.100.100.100", false},
		{"cgnat low edge", "100.64.0.1", false},
		{"cgnat high edge", "100.127.255.254", false},
		{"unspecified v4", "0.0.0.0", false},
		{"this-network 0/8", "0.1.2.3", false},
		{"ietf protocol 192.0.0/24", "192.0.0.8", false},
		{"test-net-1", "192.0.2.1", false},
		{"benchmark 198.18/15", "198.18.0.1", false},
		{"benchmark 198.19", "198.19.255.255", false},
		{"test-net-2", "198.51.100.1", false},
		{"test-net-3", "203.0.113.1", false},
		{"reserved 240/4", "240.0.0.1", false},
		{"limited broadcast", "255.255.255.255", false},
		{"multicast v4", "224.0.0.1", false},
		{"loopback v6", "::1", false},
		{"unspecified v6", "::", false},
		{"link-local v6", "fe80::1", false},
		{"link-local v6 with zone", "fe80::1%eth0", false},
		{"unique local v6", "fd7a:115c:a1e0::1", false},
		{"unique local fc00", "fc00::1", false},
		{"multicast v6", "ff02::1", false},
		{"interface-local multicast v6", "ff01::1", false},
		{"v4-mapped loopback", "::ffff:127.0.0.1", false},
		{"v4-mapped metadata", "::ffff:169.254.169.254", false},
		{"v4-mapped private", "::ffff:10.0.0.1", false},
		{"nat64 loopback", "64:ff9b::7f00:1", false},
		{"nat64 local-use", "64:ff9b:1::1", false},
		{"documentation v6", "2001:db8::1", false},
		{"v4-compatible loopback", "::7f00:1", false},
		{"6to4 embedding loopback", "2002:7f00:1::1", false},
		{"teredo", "2001:0:4136:e378:8000:63bf:3fff:fdd2", false},
		{"site-local deprecated", "fec0::1", false},
		{"documentation 3fff::/20", "3fff:abc::1", false},
		{"cloudflare v4", "1.1.1.1", true},
		{"google v4", "8.8.8.8", true},
		{"cloudflare v6", "2606:4700:4700::1111", true},
		{"v4-mapped public", "::ffff:8.8.8.8", true},
		{"just outside cgnat", "100.128.0.1", true},
		{"just outside 172.16/12", "172.32.0.1", true},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			addr, err := netip.ParseAddr(testCase.address)
			if err != nil {
				testInstance.Fatalf("parse %q: %v", testCase.address, err)
			}
			if got := IsPublicAddr(addr); got != testCase.want {
				testInstance.Errorf("IsPublicAddr(%s) = %v, want %v", testCase.address, got, testCase.want)
			}
		})
	}
}

func TestIsPublicAddrRejectsZeroValue(testInstance *testing.T) {
	if IsPublicAddr(netip.Addr{}) {
		testInstance.Error("the zero netip.Addr must not be treated as public")
	}
}
