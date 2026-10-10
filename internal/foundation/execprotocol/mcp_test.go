package execprotocol

import "testing"

func TestMCPRemoteRequiresAnExplicitUnambiguousTransport(t *testing.T) {
	for _, value := range []MCPRemote{
		{}, {Transport: "stdio"}, {Transport: "http", URL: "http://localhost:8765/mcp", Headers: map[string]string{"Authorization": "Bearer test"}}, {Transport: "sse", URL: "https://example.test/sse"},
	} {
		if err := value.Validate(); err != nil {
			t.Fatal(value.Kind(), err)
		}
	}
	for _, value := range []MCPRemote{
		{URL: "https://example.test/mcp"}, {Transport: "other"}, {Transport: "http"}, {Transport: "http", URL: "file:///tmp/socket"}, {Transport: "http", URL: "https://user:secret@example.test/mcp"}, {Transport: "http", URL: "https://example.test/mcp#fragment"}, {Transport: "http", URL: "https://example.test/mcp", Headers: map[string]string{"Authorization": "a\r\nExtra: injected"}}, {Transport: "http", URL: "https://example.test/mcp", Headers: map[string]string{"Mcp-Session-Id": "forged"}}, {Transport: "http", URL: "https://example.test/mcp", Headers: map[string]string{"Authorization": "one", "authorization": "two"}},
	} {
		if value.Validate() == nil {
			t.Fatal("accepted invalid transport", value.Kind())
		}
	}
}
