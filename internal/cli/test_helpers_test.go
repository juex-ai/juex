package cli

import (
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/config"
)

func ensureTestWorkspaceAgent(t *testing.T, work string) config.Config {
	t.Helper()
	testHome := filepath.Join(filepath.Dir(work), "juex-home")
	t.Setenv("JUEX_HOME", testHome)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(testHome, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	cfg, err := config.LoadWithOptions(config.LoadOptions{WorkDir: work, AgentState: config.AgentStateMint})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
