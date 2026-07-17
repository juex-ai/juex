// Package endpoint owns the local transport used to address a running agent.
package endpoint

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Target is a validated local agent endpoint.
type Target struct {
	network string
	address string
}

// Parse validates and normalizes a unix:// or loopback tcp:// endpoint.
func Parse(raw string) (Target, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return Target{}, fmt.Errorf("endpoint: parse %q: %w", raw, err)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Target{}, fmt.Errorf("endpoint: %q must not contain user info, query, or fragment", raw)
	}
	switch parsed.Scheme {
	case "unix":
		if parsed.Host != "" || parsed.Path == "" {
			return Target{}, fmt.Errorf("endpoint: unix target %q must contain an absolute path", raw)
		}
		path := unixPathFromURL(parsed.Path)
		if !filepath.IsAbs(path) {
			return Target{}, fmt.Errorf("endpoint: unix target %q must contain an absolute path", raw)
		}
		return Target{network: "unix", address: filepath.Clean(path)}, nil
	case "tcp":
		if parsed.Path != "" {
			return Target{}, fmt.Errorf("endpoint: tcp target %q must not contain a path", raw)
		}
		host, portText, err := net.SplitHostPort(parsed.Host)
		if err != nil {
			return Target{}, fmt.Errorf("endpoint: parse tcp target %q: %w", raw, err)
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return Target{}, fmt.Errorf("endpoint: tcp target %q must use a numeric loopback address", raw)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return Target{}, fmt.Errorf("endpoint: tcp target %q has invalid port", raw)
		}
		return Target{network: "tcp", address: net.JoinHostPort(ip.String(), strconv.Itoa(port))}, nil
	default:
		return Target{}, fmt.Errorf("endpoint: unsupported scheme %q", parsed.Scheme)
	}
}

func (t Target) Network() string {
	return t.network
}

func (t Target) Address() string {
	return t.address
}

func (t Target) URI() string {
	if t.network == "unix" {
		return unixURI(t.address)
	}
	return (&url.URL{Scheme: "tcp", Host: t.address}).String()
}

func (t Target) URL(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	requestTarget, err := url.ParseRequestURI(path)
	if err != nil {
		return (&url.URL{Scheme: "http", Host: "juex", Path: path}).String()
	}
	return (&url.URL{
		Scheme:     "http",
		Host:       "juex",
		Path:       requestTarget.Path,
		RawPath:    requestTarget.RawPath,
		ForceQuery: requestTarget.ForceQuery,
		RawQuery:   requestTarget.RawQuery,
	}).String()
}

func (t Target) DialContext(ctx context.Context) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, t.network, t.address)
}

func (t Target) NewTransport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return t.DialContext(ctx)
		},
	}
}

func (t Target) NewClient() *http.Client {
	return &http.Client{Transport: t.NewTransport()}
}

func unixURI(path string) string {
	slashPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(slashPath) >= 2 && slashPath[1] == ':' {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "unix", Path: slashPath}).String()
}

func unixPathFromURL(path string) string {
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = strings.TrimPrefix(path, "/")
	}
	return filepath.FromSlash(path)
}
