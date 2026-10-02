package clientip

import (
	"net/http/httptest"
	"testing"
)

func TestAddressTrustBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, trusted, peer, forwarded, want string
	}{
		{"default untrusted", "", "127.0.0.1:1234", "192.0.2.1", "127.0.0.1:1234"},
		{"untrusted spoof", "172.30.0.11", "192.0.2.2:1234", "192.0.2.1", "192.0.2.2:1234"},
		{"trusted", "172.30.0.11", "172.30.0.11:1234", "192.0.2.1", "192.0.2.1:1234"},
		{"trusted CIDR", "172.30.0.11/32, 2001:db8:1::/64", "[2001:db8:1::2]:1234", "2001:db8:2::3", "[2001:db8:2::3]:1234"},
		{"mapped peer", "172.30.0.11", "[::ffff:172.30.0.11]:1234", "::ffff:192.0.2.1", "192.0.2.1:1234"},
		{"missing", "172.30.0.11", "172.30.0.11:1234", "", "172.30.0.11:1234"},
		{"chain rejected", "172.30.0.11", "172.30.0.11:1234", "192.0.2.1, 192.0.2.2", "172.30.0.11:1234"},
		{"hostname rejected", "172.30.0.11", "172.30.0.11:1234", "client.example", "172.30.0.11:1234"},
		{"zone rejected", "172.30.0.11", "172.30.0.11:1234", "fe80::1%eth0", "172.30.0.11:1234"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver, err := New(tc.trusted)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("POST", "/", nil)
			request.RemoteAddr = tc.peer
			if tc.forwarded != "" {
				request.Header.Set("X-Real-IP", tc.forwarded)
			}
			request.Header.Set("X-Forwarded-For", "198.51.100.1")
			if got := resolver.Address(request); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAmbiguousClientAddressIsNotTrusted(t *testing.T) {
	resolver, err := New("172.30.0.11")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.RemoteAddr = "172.30.0.11:1234"
	request.Header.Add("X-Real-IP", "192.0.2.1")
	request.Header.Add("X-Real-IP", "192.0.2.2")
	if got := resolver.Address(request); got != request.RemoteAddr {
		t.Fatal(got)
	}
}

func TestInvalidTrustedProxyConfiguration(t *testing.T) {
	for _, value := range []string{"proxy.example", "172.30.0.11:80", "172.30.0.11,", "172.30.0.0/99", "fe80::1%eth0", "::ffff:172.30.0.11/128"} {
		if _, err := New(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}
