// Package clientip resolves client addresses behind explicitly trusted proxies.
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type Resolver struct{ trusted []netip.Prefix }

// New accepts a comma-separated list of proxy IP addresses or CIDRs. Empty
// configuration trusts no proxy, including one on the loopback interface.
func New(configuration string) (Resolver, error) {
	var result Resolver
	if strings.TrimSpace(configuration) == "" {
		return result, nil
	}
	for _, value := range strings.Split(configuration, ",") {
		value = strings.TrimSpace(value)
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, addressErr := netip.ParseAddr(value)
			if addressErr != nil || address.Zone() != "" {
				return Resolver{}, fmt.Errorf("invalid trusted proxy address or CIDR %q", value)
			}
			address = address.Unmap()
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix.Addr().Is4In6() {
			return Resolver{}, fmt.Errorf("use an IPv4 CIDR for trusted proxy %q", value)
		}
		result.trusted = append(result.trusted, prefix.Masked())
	}
	return result, nil
}

// Address preserves RemoteAddr's host:port shape for per-IP limiters. Only a
// trusted TCP peer may replace its host with one X-Real-IP value. The edge proxy
// must overwrite that header with its own peer address; no header chain is used.
func (r Resolver) Address(request *http.Request) string {
	host, port, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return request.RemoteAddr
	}
	peer = peer.Unmap()
	for _, prefix := range r.trusted {
		if !prefix.Contains(peer) {
			continue
		}
		values := request.Header.Values("X-Real-IP")
		if len(values) != 1 {
			return request.RemoteAddr
		}
		client, err := netip.ParseAddr(values[0])
		if err != nil || client.Zone() != "" {
			return request.RemoteAddr
		}
		return net.JoinHostPort(client.Unmap().String(), port)
	}
	return request.RemoteAddr
}
