package migration

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
)

var errMCPSource = errors.New("source MCP transport, endpoint or headers are invalid or unproved")

func sourceMCPTransport(data []byte) (string, error) {
	fields, err := extensionObject(data, "type", "command", "args", "env", "url", "headers")
	if err != nil {
		return "", errMCPSource
	}
	kind := "stdio"
	if raw, ok := fields["type"]; ok {
		kind, err = extensionString(raw)
		if err != nil {
			return "", errMCPSource
		}
		kind = strings.TrimSpace(kind)
	}
	switch kind {
	case "stdio":
		return kind, nil
	case "http", "streamable-http":
		return "http", nil
	default:
		return "", errMCPSource
	}
}

func convertHTTPServer(name string, data []byte, environment map[string]*string) (extensionpolicy.MCPResource, error) {
	var empty extensionpolicy.MCPResource
	fields, err := extensionObject(data, "type", "url", "headers")
	if err != nil {
		return empty, errMCPSource
	}
	endpoint, err := extensionString(fields["url"])
	if err != nil {
		return empty, errMCPSource
	}
	u, err := url.Parse(endpoint)
	if err != nil || !u.IsAbs() || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return empty, errMCPSource
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	secure := strings.EqualFold(u.Scheme, "https")
	loopbackHTTP := strings.EqualFold(u.Scheme, "http") && (host == "localhost" || ip != nil && ip.IsLoopback())
	if !secure && !loopbackHTTP {
		return empty, errMCPSource
	}
	result := extensionpolicy.MCPResource{CommandResource: extensionpolicy.CommandResource{ID: name}, MCPRemote: execprotocol.MCPRemote{Transport: "http", URL: endpoint}}
	if raw, ok := fields["headers"]; ok {
		headers, err := extensionObject(raw)
		if err != nil {
			return empty, errMCPSource
		}
		result.Headers = make(map[string]string, len(headers))
		for key, raw := range headers {
			value, err := extensionString(raw)
			if err != nil || strings.ContainsAny(value, "\r\n") {
				return empty, errMCPSource
			}
			value, err = resolveMCPHeader(value, environment)
			if err != nil || !utf8.ValidString(value) {
				return empty, errMCPSource
			}
			result.Headers[key] = value
		}
	}
	if result.Validate() != nil {
		return empty, errMCPSource
	}
	return result, nil
}

// Fixed-source header templates expand once. A fallback still needs evidence
// of a missing/empty source value; unknown capture must never select it.
func resolveMCPHeader(input string, environment map[string]*string) (string, error) {
	var out strings.Builder
	for offset := 0; offset < len(input); {
		start := strings.Index(input[offset:], "${")
		if start < 0 {
			out.WriteString(input[offset:])
			break
		}
		start += offset
		out.WriteString(input[offset:start])
		end := strings.IndexByte(input[start+2:], '}')
		if end < 0 {
			out.WriteString(input[start:])
			break
		}
		end += start + 2
		expression := input[start+2 : end]
		name, fallback, hasFallback := strings.Cut(expression, ":-")
		valid := name != "" && (name[0] == '_' || name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 'a' && name[0] <= 'z')
		for i := 1; valid && i < len(name); i++ {
			valid = extensionEnvByte(name[i])
		}
		if !valid {
			out.WriteString(input[start : end+1])
			offset = end + 1
			continue
		}
		value, known := environment[name]
		if !known {
			return "", errMCPSource
		}
		if value == nil || *value == "" {
			if !hasFallback {
				return "", errMCPSource
			}
			out.WriteString(fallback)
		} else {
			out.WriteString(*value)
		}
		offset = end + 1
	}
	return out.String(), nil
}
