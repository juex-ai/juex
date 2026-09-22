//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/memory"
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/framework/agentstate"
)

func TestLiveConfigs_FleetMemorySupervisorCommit(t *testing.T) {
	selected := loadLiveConfigs(t)[0]
	home := t.TempDir()
	api, user := startMemoryFixture(t, home, mc.Basic)
	cfg := selected.cfg
	cfg.Preset = config.PresetMinimal
	cfg.Modules = config.ModulePolicy{memory.ModuleID: {Enabled: true}}
	cfg.HomeJuexDir, cfg.AgentID = home, "source-agent"
	cfg.AgentStateDir = filepath.Join(home, "agents", cfg.AgentID)
	cfg.AgentAddress = agentstate.AgentAddress{}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	a := memoryApp(t, cfg, nil)
	if out, err := a.Run(ctx, "Explicitly remember this stable preference: in this workspace I want release notes in Simplified Chinese."); err != nil || strings.TrimSpace(out) == "" {
		t.Fatalf("source provider %s: %v", selected.name, err)
	}
	var receipt mc.Receipt
	for _, m := range a.Thread.History {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && !b.IsError {
				var candidate mc.Receipt
				if json.Unmarshal([]byte(b.Content), &candidate) == nil && candidate.ID != "" && candidate.State == "pending" {
					receipt = candidate
				}
			}
		}
	}
	if receipt.ID == "" {
		t.Fatal("live source did not obtain a proposal receipt")
	}
	persisted, err := api.Result(ctx, user, receipt.ID)
	if err != nil || persisted.State != "pending" || persisted.Attempts != 0 {
		t.Fatalf("live source did not finish before background review: %+v, %v", persisted, err)
	}
	cfg.AgentID, cfg.MemoryProfile = "supervisor", mc.ProfileSupervisor
	cfg.AgentStateDir = filepath.Join(home, "agents", cfg.AgentID)
	cfg.Modules = config.ModulePolicy{memory.ModuleID: {Enabled: true}, "worker-threads": {Enabled: true}}
	_ = memoryApp(t, cfg, nil)
	for {
		var err error
		receipt, err = api.Result(ctx, user, receipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Committed {
			break
		}
		if receipt.State == "rejected" || receipt.State == "failed" || receipt.State == "no_change" {
			t.Fatalf("live review did not commit: %+v", receipt)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("live review timed out: %+v", receipt)
		case <-time.After(250 * time.Millisecond):
		}
	}
	if receipt.Attempts != 1 {
		t.Fatalf("live review required repeated Workers: %+v", receipt)
	}
	cfg.WorkDir = t.TempDir()
	cfg.AgentID, cfg.MemoryProfile = "reader-agent", mc.ProfileAgent
	cfg.AgentStateDir = filepath.Join(home, "agents", cfg.AgentID)
	cfg.Modules = config.ModulePolicy{memory.ModuleID: {Enabled: true}}
	b := memoryApp(t, cfg, nil)
	read, ok := b.Engine.Tools.Get(memory.ToolRead)
	if !ok || len(receipt.EntryIDs) == 0 {
		t.Fatal("committed entry is missing")
	}
	body, err := read.Handler(ctx, map[string]any{"id": receipt.EntryIDs[0]})
	if err != nil || (!strings.Contains(strings.ToLower(body), "chinese") && !strings.Contains(body, "中文")) {
		t.Fatalf("cross-Agent committed knowledge: %s %v", body, err)
	}
	t.Logf("Fleet Memory live chain: provider=%s receipt=%s state=%s entries=%d", selected.name, receipt.ID, receipt.State, len(receipt.EntryIDs))
}
