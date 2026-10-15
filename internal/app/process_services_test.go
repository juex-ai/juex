package app

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/features/mcp"
)

func TestProcessMCPStartupReplacesPriorDiagnostics(t *testing.T) {
	work := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JUEX_HOME", t.TempDir())
	mustWriteAppTestFile(t, filepath.Join(work, ".agents", "mcp.json"), `{"mcpServers":{"alpha":{"command":"__juex_missing_mcp_command__"}}}`)
	services := NewProcessServices(ProcessServicesOptions{Config: config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: work, AgentStateDir: t.TempDir()}, Stderr: io.Discard})
	t.Cleanup(services.Close)
	services.recordMCPError(&mcp.ServerError{Server: "alpha", Op: "connect", Err: errors.New("old failure")})
	if err := services.EnsureMCPStarted(t.Context()); err != nil {
		t.Fatal(err)
	}
	diagnostic := services.mcpErrors()["alpha"]
	if strings.Contains(diagnostic, "old failure") || !strings.Contains(diagnostic, "resolve command") {
		t.Fatalf("startup diagnostic = %q", diagnostic)
	}
}
