package execprotocol

import (
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

// MCPRemote selects an explicit wire transport. HTTP is initiated by the
// selected executor, never by Management or the browser.
type MCPRemote struct {
	Transport string            `json:"transport,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

func (v MCPRemote) Kind() string {
	if v.Transport == "" {
		return "stdio"
	}
	return v.Transport
}

func (v MCPRemote) Validate() error {
	if v.Kind() == "stdio" {
		if v.URL != "" || len(v.Headers) != 0 {
			return ErrInvalid
		}
		return nil
	}
	if v.Kind() != "http" && v.Kind() != "sse" {
		return ErrInvalid
	}
	u, err := url.Parse(v.URL)
	if err != nil || len(v.URL) > 8192 || !utf8.ValidString(v.URL) || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" || len(v.Headers) > 32 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for name, value := range v.Headers {
		key := strings.ToLower(name)
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) || len(value) > 8192 || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
		switch http.CanonicalHeaderKey(name) {
		case "Host", "Connection", "Content-Length", "Transfer-Encoding", "Upgrade", "Content-Type", "Accept", "Mcp-Session-Id", "Mcp-Protocol-Version":
			return ErrInvalid
		}
	}
	return nil
}

// MCPConnection records successful protocol initialization independently of
// retained output. A historical handshake alone does not prove a live session.
type MCPConnection struct {
	Transport       string    `json:"transport"`
	ConnectedAt     time.Time `json:"connected_at"`
	ServerName      string    `json:"server_name"`
	ServerVersion   string    `json:"server_version"`
	ProtocolVersion string    `json:"protocol_version"`
}

func (v MCPConnection) Validate() error {
	if v.Transport != "stdio" && v.Transport != "http" && v.Transport != "sse" || v.ConnectedAt.IsZero() {
		return ErrInvalid
	}
	for _, value := range []string{v.ServerName, v.ServerVersion, v.ProtocolVersion} {
		if !utf8.ValidString(value) || len([]rune(value)) > 512 {
			return ErrInvalid
		}
	}
	return nil
}
