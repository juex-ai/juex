package extensionpolicy

import (
	"fmt"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
)

func TestMCPManifestKeepsHTTPAndProcessConfigurationSeparate(t *testing.T) {
	manifest := Manifest{ManifestVersion: 2, Name: "remote", Version: "1", MCP: []MCPResource{{CommandResource: CommandResource{ID: "service"}, MCPRemote: execprotocol.MCPRemote{Transport: "http", URL: "http://localhost:8900/mcp", Headers: map[string]string{"Authorization": "Bearer test"}}}}}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	manifest.MCP[0].Command = []string{"unused"}
	if manifest.Validate() == nil {
		t.Fatal("mixed HTTP and process resource")
	}
	manifest.MCP[0].Command = nil
	manifest.MCP[0].Environment = map[string]string{"TOKEN": "unused"}
	if manifest.Validate() == nil {
		t.Fatal("HTTP process environment silently ignored")
	}
}

func TestManifestRejectsMergedEnvironmentThatCannotExecute(t *testing.T) {
	defaults := map[string]string{}
	for i := 0; i < 64; i++ {
		defaults[fmt.Sprintf("VAR_%d", i)] = "value"
	}
	manifest := Manifest{ManifestVersion: 2, Name: "example", Version: "1", Environment: defaults, Hooks: []hookpolicy.Declaration{{ID: "sample", Enabled: true, Events: []hookpolicy.Event{hookpolicy.Stop}, Command: []string{"/bin/true"}, Environment: map[string]string{"EXTRA": "value"}}}}
	if manifest.Validate() == nil {
		t.Fatal("accepted 65 merged environment keys")
	}
	delete(defaults, "VAR_63")
	if err := manifest.Validate(); err != nil {
		t.Fatal("valid merged environment", err)
	}
	manifest.Hooks[0].Environment = map[string]string{"JUEX_EXT_DIR": "forged"}
	if manifest.Validate() == nil {
		t.Fatal("accepted reserved environment override")
	}
}
