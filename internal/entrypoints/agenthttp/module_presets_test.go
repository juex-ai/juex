package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
)

func TestRunRejectsUnknownModuleBeforeReadinessAndMCPStartup(t *testing.T) {
	srv := newTestServer(t)
	setTestAgentAddress(t, &srv.opts.Cfg)
	srv.opts.Cfg.Modules = config.ModulePolicy{"invalid-module": {Enabled: true}}
	marker := filepath.Join(t.TempDir(), "mcp-started")
	mustWriteWebFakeMCPConfigEnv(t, srv.opts.Cfg.WorkDir, false, map[string]string{
		"JUEX_WEB_FAKE_MCP_LIST_MARKER": marker,
	})
	ready := false
	srv.opts.OnReady = func(ReadyInfo) { ready = true }
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := srv.Run(ctx); err == nil || !strings.Contains(err.Error(), "unsupported module") {
		t.Fatalf("Run error = %v, want unknown module", err)
	}
	if ready {
		t.Error("invalid configuration advertised readiness")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("invalid configuration launched MCP: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.AgentStateDir, "threads")); !os.IsNotExist(err) {
		t.Errorf("invalid configuration created Thread state: %v", err)
	}
}
