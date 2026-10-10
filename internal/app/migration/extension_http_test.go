package migration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestConvertHTTPMCPExtensionFrozenHeaders(t *testing.T) {
	t.Setenv("TOKEN", "wrong-migration-process-token")
	for _, transport := range []string{"http", "streamable-http", " http "} {
		servers := `{"mcpServers":{"remote":{"type":` + strconvJSON(transport) + `,"url":"https://example.invalid/mcp?static=${TOKEN}","headers":{"Authorization":"Bearer ${TOKEN}","X-Fallback":"${ABSENT:-fallback}/${EMPTY:-}/${EMPTY:-empty}","X-Literal":"$TOKEN/${bad-name}/${BROKEN"}},"wire":{"command":"wire"}}}`
		manifest, err := ConvertMCPExtension(stdioSnapshot(stdioManifest, servers), stdioBindings(), map[string]*string{"TOKEN": textPointer("source-${OTHER}"), "ABSENT": nil, "EMPTY": textPointer("")})
		if err != nil {
			t.Fatal(err)
		}
		remote := manifest.MCP[0]
		if remote.ID != "remote" || remote.Transport != "http" || remote.URL != "https://example.invalid/mcp?static=${TOKEN}" || remote.Headers["Authorization"] != "Bearer source-${OTHER}" || remote.Headers["X-Fallback"] != "fallback//empty" || remote.Headers["X-Literal"] != "$TOKEN/${bad-name}/${BROKEN" || len(remote.Command) != 0 || len(remote.Environment) != 0 {
			t.Fatal("changed source HTTP semantics")
		}
		if manifest.MCP[1].Transport != "" {
			t.Fatal("changed retained stdio encoding")
		}
	}
}

func strconvJSON(value string) string { raw, _ := json.Marshal(value); return string(raw) }

func TestConvertHTTPMCPRejectsUnprovedSourceAndTarget(t *testing.T) {
	for _, newline := range []string{"\n", "\r"} {
		server, _ := json.Marshal(map[string]any{"type": "http", "url": "https://example.invalid", "headers": map[string]string{"Authorization": "${TOKEN:-invalid" + newline + "fallback}"}})
		_, err := ConvertMCPExtension(stdioSnapshot(stdioManifest, `{"mcpServers":{"remote":`+string(server)+`}}`), nil, map[string]*string{"TOKEN": textPointer("valid-known-token")})
		if err == nil {
			t.Fatal("accepted a source-invalid header hidden by a known value")
		}
	}
	for name, server := range map[string]string{
		"implicit URL":     `{"url":"https://example.invalid"}`,
		"legacy SSE":       `{"type":"sse","url":"https://example.invalid"}`,
		"type case":        `{"type":"HTTP","url":"https://example.invalid"}`,
		"nonloopback":      `{"type":"http","url":"http://example.invalid"}`,
		"userinfo":         `{"type":"http","url":"https://secret@example.invalid"}`,
		"fragment":         `{"type":"http","url":"https://example.invalid/#secret"}`,
		"process command":  `{"type":"http","url":"https://example.invalid","command":null}`,
		"process args":     `{"type":"http","url":"https://example.invalid","args":[]}`,
		"process env":      `{"type":"http","url":"https://example.invalid","env":{}}`,
		"null headers":     `{"type":"http","url":"https://example.invalid","headers":null}`,
		"null header":      `{"type":"http","url":"https://example.invalid","headers":{"Authorization":null}}`,
		"duplicate header": `{"type":"http","url":"https://example.invalid","headers":{"X-Key":"a","X-Key":"b"}}`,
		"case header":      `{"type":"http","url":"https://example.invalid","headers":{"X-Key":"a","x-key":"b"}}`,
		"reserved header":  `{"type":"http","url":"https://example.invalid","headers":{"Mcp-Session-Id":"secret"}}`,
		"unknown fallback": `{"type":"http","url":"https://example.invalid","headers":{"Authorization":"${UNKNOWN:-private-token}"}}`,
		"unknown field":    `{"type":"http","url":"https://example.invalid","oauth":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ConvertMCPExtension(stdioSnapshot(stdioManifest, `{"mcpServers":{"remote":`+server+`}}`), nil, nil)
			if err == nil || strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "example.invalid") {
				t.Fatal("accepted invalid source or leaked credential", err)
			}
		})
	}
	for _, value := range []*string{nil, textPointer(""), textPointer("secret\r\nX-Inject: value"), textPointer("secret\xff"), textPointer(strings.Repeat("s", 8193))} {
		_, err := ConvertMCPExtension(stdioSnapshot(stdioManifest, `{"mcpServers":{"remote":{"type":"http","url":"https://example.invalid","headers":{"Authorization":"${TOKEN}"}}}}`), nil, map[string]*string{"TOKEN": value})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid header evidence accepted or leaked", err)
		}
	}
}

func TestMCPHeaderEvidencePrivateRoundTripAndDigest(t *testing.T) {
	_, inputs, _ := bundleFixture(t)
	snapshot := stdioSnapshot(stdioManifest, `{"mcpServers":{"remote":{"type":"http","url":"https://example.invalid","headers":{"Authorization":"${TOKEN}","X-Missing":"${ABSENT:-yes}"}}}}`)
	inputs.Extensions = []BundleExtension{{AgentID: "abc234", Snapshot: snapshot, HeaderEnvironment: map[string]*string{"TOKEN": textPointer("private-frozen-token"), "ABSENT": nil, "EMPTY": textPointer("")}}}
	data, err := encodeBundleInputs(inputs)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := decodeBundleInputs(data)
	if err != nil || !reflect.DeepEqual(inputs, restored) {
		t.Fatal("lost private tri-state evidence", err)
	}
	printed, _ := json.Marshal(inputs.Extensions[0])
	if strings.Contains(string(printed), "private-frozen-token") {
		t.Fatal("printed private effective environment")
	}
	inputs.Extensions[0].HeaderEnvironment["TOKEN"] = textPointer("different-private-token")
	changed, err := encodeBundleInputs(inputs)
	if err != nil || bundleDigest(data) == bundleDigest(changed) {
		t.Fatal("private evidence not bound to digest", err)
	}
}

func TestStdioMCPRetainsCompleteManifestAndHostImport(t *testing.T) {
	manifest, err := ConvertMCPExtension(stdioSnapshot(stdioManifest, stdioServers), stdioBindings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Independently spell the original fixed stdio output, including nil/empty
	// encoding and launcher arguments; HTTP support must not change its receipts.
	const expected = `{"manifest_version":2,"name":"wire","version":"1.2.3","description":"Selected wire","skills":null,"hooks":null,"mcp":[{"id":"wire","command":["/bin/sh","-p","-c","export WORKDIR=\"$1\" JUEX_WORKDIR=\"$1\" \u0026\u0026 cd -- \"$2\" \u0026\u0026 shift 2 \u0026\u0026 exec \"$@\"","juex-stdio","/work","/cwd","/bin/wire","mcp","two words","","$HOME","$(touch marker)","/work/a","$WORKDIR_X","/work/b"],"environment":{"CWD":"/work","ROOT":"${JUEX_EXT_DIR}","STATE":"${JUEX_EXT_DATA_DIR}/state"}}],"observables":null}`
	encoded, _ := json.Marshal(manifest)
	if string(encoded) != expected {
		t.Fatalf("retained manifest bytes differ: %s", encoded)
	}
	var original extensionpolicy.Manifest
	if err := json.Unmarshal([]byte(expected), &original); err != nil {
		t.Fatal(err)
	}
	bag := &Bundle{digest: bundleDigest([]byte("stdio-original"))}
	source := legacy.Agent{Definition: legacy.AgentDefinition{ID: "source"}}
	owner := uuid.MustParse("11111111-1111-4111-8111-111111111111").String()
	requests := make([][]byte, 0, 2)
	for _, candidate := range []extensionpolicy.Manifest{original, manifest} {
		request, err := bag.hostFiles(source, owner, PreparedBundle{Extensions: map[string][]extensionpolicy.Manifest{"source": {candidate}}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(request)
		requests = append(requests, encoded)
	}
	if string(requests[0]) != string(requests[1]) {
		t.Fatal("changed full HostImport wire")
	}
}
