package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/framework/agentstate"
)

// Run with the Calendar executable built from juex-extensions. The core suite
// otherwise exercises the same generic MCP boundary with its protocol fixtures.
func TestCalendarExtensionIntegration(t *testing.T) {
	binary := os.Getenv("JUEX_CALENDAR_BINARY")
	if binary == "" {
		t.Skip("set JUEX_CALENDAR_BINARY to the built external Calendar executable")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatal(err)
	}
	home, work := t.TempDir(), t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(home, "extensions", "calendar")
	writeE2EConfig(t, filepath.Join(ext, "juex.extension.json"), `{"manifest_version":1,"name":"calendar","version":"1.0.0"}`)
	body, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"calendar": map[string]any{"command": binary, "args": []string{"mcp"}, "env": map[string]string{"JUEX_EXT_DATA_DIR": "${JUEX_EXT_DATA_DIR}"}}}})
	if err != nil {
		t.Fatal(err)
	}
	writeE2EConfig(t, filepath.Join(ext, "mcp.json"), string(body))
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), Preset: config.PresetMinimal, WorkDir: work, HomeJuexDir: home, AgentAddress: address,
		Modules:    config.ModulePolicy{"extensions": {Enabled: true}, "mcp": {Enabled: true}},
		Extensions: config.ExtensionPolicy{Allow: []string{"calendar"}, Configured: true},
	}
	provider := &moduleEntryProvider{}
	a, err := app.New(app.Options{Config: cfg, Provider: provider, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"calendar_list", "calendar_create", "calendar_update", "calendar_delete"} {
		if _, ok := a.Engine.Tools.Get("mcp__calendar__" + name); !ok {
			t.Fatalf("missing extension tool %s", name)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	_, err = a.Engine.Tools.Call(ctx, "mcp__calendar__calendar_create", map[string]any{"id": "integration", "content": "calendar integration reminder", "once": map[string]any{"at": time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano)}})
	if err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 10*time.Second, func() bool { return provider.calls.Load() == 1 })
	result, err := a.Engine.Tools.Call(ctx, "mcp__calendar__calendar_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(encoded), "last_sent_at") {
		t.Fatalf("calendar status: %s %v", encoded, err)
	}
	if _, err := os.Stat(filepath.Join(address.StateDir(), "extensions", "calendar", "calendar.json")); err != nil {
		t.Fatalf("Agent-private Calendar state: %v", err)
	}
}
