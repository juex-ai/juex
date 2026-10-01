package hosted

import (
	"net/netip"
	"strings"
	"testing"
)

func TestLANExceptionsCannotOverrideAgentOrPlatformIsolation(t *testing.T) {
	policy := Policy{Bridge: "jx0123456789ab", Subnet: "172.30.0.0/28", HostedPool: netip.MustParsePrefix("172.30.0.0/16"), Control: netip.MustParseAddrPort("192.168.1.2:8684"), Protected: []netip.Prefix{netip.MustParsePrefix("172.19.0.0/16")}}
	for _, address := range []string{"172.30.0.2", "172.19.0.4", "169.254.169.254", "127.0.0.1", "::1"} {
		policy.Allow = []Endpoint{{Address: netip.MustParseAddr(address), Protocol: "tcp", Port: 443}}
		if err := policy.validate(); err == nil {
			t.Fatal("unsafe exception admitted", address)
		}
	}
	policy.Allow = []Endpoint{{Address: netip.MustParseAddr("100.64.12.34"), Protocol: "tcp", Port: 8317}}
	if err := policy.validate(); err != nil {
		t.Fatal("explicit business endpoint denied", err)
	}
	policy.Bridge = "br0; echo bad"
	if err := policy.validate(); err == nil {
		t.Fatal("arbitrary interface accepted")
	}
}

func TestDNSCannotBypassProtectedDestinations(t *testing.T) {
	config := Config{Pool: netip.MustParsePrefix("172.30.0.0/16"), Protected: []netip.Prefix{netip.MustParsePrefix("172.19.0.0/16")}}
	for _, address := range []string{"127.0.0.11", "169.254.169.254", "172.30.0.2", "172.19.0.2", "::1"} {
		config.DNS = []netip.Addr{netip.MustParseAddr(address)}
		if _, _, err := resolver(config); err == nil {
			t.Fatal("unsafe DNS accepted", address)
		}
	}
	config.DNS = []netip.Addr{netip.MustParseAddr("10.0.2.3")}
	content, endpoints, err := resolver(config)
	if err != nil || !strings.Contains(content, "nameserver 10.0.2.3\n") || len(endpoints) != 2 {
		t.Fatal(content, endpoints, err)
	}
	for _, endpoint := range endpoints {
		if endpoint.Port != 53 {
			t.Fatal("DNS grants unrelated ports", endpoint)
		}
	}
}
