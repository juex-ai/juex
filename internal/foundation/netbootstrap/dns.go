package netbootstrap

import (
	"context"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// envProbe abstracts the filesystem and environment lookups so the
// resolver-selection logic can be unit-tested without touching the real
// /etc/resolv.conf or process environment.
type envProbe struct {
	stat     func(string) (os.FileInfo, error)
	readFile func(string) ([]byte, error)
	getenv   func(string) string
}

var realProbe = envProbe{
	stat:     os.Stat,
	readFile: os.ReadFile,
	getenv:   os.Getenv,
}

// findNameservers picks fallback DNS servers when the system's pure-Go
// resolver would otherwise fail. It returns nil when the system already has
// a usable /etc/resolv.conf, or when no override signal is available.
//
// Resolution order (when /etc/resolv.conf is missing):
//  1. $JUEX_DNS — explicit user override, comma-separated host[:port] entries
//  2. $PREFIX/etc/resolv.conf — Termux's standard config location
//  3. nil — let the original resolver error surface
func findNameservers(p envProbe) []string {
	if _, err := p.stat("/etc/resolv.conf"); err == nil {
		return nil
	}
	// Any stat error (missing, permission, IO) → proceed to overrides.
	// The Go resolver falls back to ::1:53 in those cases anyway.

	if list := parseHostList(p.getenv("JUEX_DNS")); len(list) > 0 {
		return list
	}

	if prefix := p.getenv("PREFIX"); prefix != "" {
		body, err := p.readFile(prefix + "/etc/resolv.conf")
		if err == nil {
			if list := parseResolvConf(body); len(list) > 0 {
				return list
			}
		}
	}

	return nil
}

// parseResolvConf extracts nameserver host:port pairs from a resolv.conf
// body. Comments (`#` / `;`) and unrelated directives are skipped.
func parseResolvConf(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	var out []string
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ns := normalizeNameserver(fields[1])
		if ns == "" {
			continue
		}
		out = append(out, ns)
	}
	return out
}

// parseHostList parses a JUEX_DNS-style comma-separated list of host[:port]
// entries.
func parseHostList(s string) []string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		ns := normalizeNameserver(strings.TrimSpace(part))
		if ns == "" {
			continue
		}
		out = append(out, ns)
	}
	return out
}

// normalizeNameserver returns a `host:port` string suitable for net.Dial,
// adding the default DNS port 53 when missing and bracketing bare IPv6
// addresses. Returns "" if the input cannot be parsed as an IP and lacks
// an explicit port.
func normalizeNameserver(s string) string {
	if s == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	if ip := net.ParseIP(s); ip != nil {
		return net.JoinHostPort(s, "53")
	}
	return ""
}

// dialFn matches the signature of (*net.Dialer).DialContext so tests can
// inject a recording fake without touching the real network stack.
type dialFn func(ctx context.Context, network, addr string) (net.Conn, error)

// makeFallbackDial returns a Dial function suitable for net.Resolver.Dial
// that round-robins across servers and retries the next server on failure.
// The network argument from the resolver (e.g. "udp" or "tcp" — the latter
// is used for the truncated-response fallback per RFC 5966) is forwarded
// unchanged to the underlying dial.
func makeFallbackDial(servers []string, dial dialFn) func(ctx context.Context, network, _ string) (net.Conn, error) {
	var idx atomic.Uint64
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		offset := int(idx.Add(1) - 1)
		var lastErr error
		for i := 0; i < len(servers); i++ {
			pick := servers[(offset+i)%len(servers)]
			conn, err := dial(ctx, network, pick)
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// applyResolver wires a fallback Dial function into r so DNS lookups go to
// the supplied servers in round-robin order. No-op when servers is empty.
func applyResolver(r *net.Resolver, servers []string) {
	if len(servers) == 0 || r == nil {
		return
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	r.PreferGo = true
	r.Dial = makeFallbackDial(servers, dialer.DialContext)
}

// Install applies fallback DNS resolution for environments without a
// usable /etc/resolv.conf (notably Termux on Android). Idempotent: safe
// to call multiple times. Called automatically via init() in
// netbootstrap.go.
func Install() {
	applyResolver(net.DefaultResolver, findNameservers(realProbe))
}
