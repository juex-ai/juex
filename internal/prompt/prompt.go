// Package prompt assembles the system prompt that opens every turn.
//
// Layout (in order, matching design doc §4.1):
//
//  1. AGENTS.md hierarchy (user-global -> project root -> cwd subdir)
//  2. Skills index (descriptions only)
//  3. Memory section (Layer 2 entries)
//  4. Tool list (auto-supplied to the provider, not duplicated here)
//  5. Operating context (cwd, time, OS)
//
// The builder is rebuilt from scratch every turn so that memory edits and
// skill changes propagate immediately.
package prompt

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/skills"
)

type Builder struct {
	GlobalAgentsMDPath string   // optional; e.g. ~/.agents/AGENTS.md
	AgentsMDDirs       []string // ordered: lowest precedence first (user dirs first, project root last)
	Memory             *memory.Store
	Skills             *skills.Loader
	Now                func() time.Time
}

// Build composes the prompt. Empty or unavailable sources are skipped
// gracefully — the resulting string is whatever applies.
func (b *Builder) Build() string {
	var sections []string

	if agents := memory.LoadAgentsMD(b.GlobalAgentsMDPath, b.AgentsMDDirs); agents != "" {
		sections = append(sections, agents)
	}

	if b.Skills != nil {
		if s := b.Skills.PromptSection(); s != "" {
			sections = append(sections, s)
		}
	}

	if b.Memory != nil {
		if mem, _ := b.Memory.PromptSection(); mem != "" {
			sections = append(sections, mem)
		}
	}

	sections = append(sections, b.operatingContext())

	return strings.Join(sections, "\n\n---\n\n")
}

func (b *Builder) operatingContext() string {
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	cwd, _ := os.Getwd()
	return fmt.Sprintf(
		"## Operating Context\n- cwd: %s\n- os: %s/%s\n- time: %s",
		cwd, runtime.GOOS, runtime.GOARCH,
		now().UTC().Format(time.RFC3339),
	)
}
