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
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/skills"
)

type Builder struct {
	GlobalAgentsMDPath string   // optional; e.g. ~/.agents/AGENTS.md
	AgentsMDDirs       []string // loaded after global AGENTS.md, in caller-provided order
	Memory             *memory.Store
	Skills             *skills.Loader
	WorkDir            string
	Now                func() time.Time
}

type Section struct {
	Key    string
	Label  string
	Source string
	Path   string
	Text   string
}

// Build composes the prompt. Empty or unavailable sources are skipped
// gracefully — the resulting string is whatever applies.
func (b *Builder) Build() string {
	return JoinSections(b.Sections())
}

func (b *Builder) Sections() []Section {
	var sections []Section
	for _, agents := range memory.LoadAgentsMDFiles(b.GlobalAgentsMDPath, b.AgentsMDDirs) {
		sections = append(sections, Section{
			Key:    "agents",
			Label:  b.agentsSectionLabel(agents.Path),
			Source: b.agentsSectionSource(agents.Path),
			Path:   agents.Path,
			Text:   agents.Text,
		})
	}

	if b.Skills != nil {
		if s := b.Skills.PromptSection(); s != "" {
			sections = append(sections, Section{Key: "skills", Label: "Available Skills", Source: "runtime", Text: s})
		}
	}

	if b.Memory != nil {
		if mem, _ := b.Memory.PromptSection(); mem != "" {
			sections = append(sections, Section{Key: "memory_files", Label: "Memory", Source: "runtime", Text: mem})
		}
	}

	sections = append(sections, Section{Key: "operating_context", Label: "Operating Context", Source: "runtime", Text: b.operatingContext()})
	return sections
}

func JoinSections(sections []Section) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		if section.Text != "" {
			parts = append(parts, section.Text)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func (b *Builder) operatingContext() string {
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	cwd := b.WorkDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	} else if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	return fmt.Sprintf(
		"## Operating Context\n- cwd: %s\n- os: %s/%s\n- time: %s",
		cwd, runtime.GOOS, runtime.GOARCH,
		now().UTC().Format(time.RFC3339),
	)
}

func (b *Builder) agentsSectionLabel(path string) string {
	if sameCleanPath(path, b.GlobalAgentsMDPath) {
		return "Global AGENTS.md"
	}
	if filepath.Base(filepath.Dir(path)) == ".agents" {
		return ".agents/AGENTS.md"
	}
	return "Workspace AGENTS.md"
}

func (b *Builder) agentsSectionSource(path string) string {
	if sameCleanPath(path, b.GlobalAgentsMDPath) {
		return "user"
	}
	return "project"
}

func sameCleanPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return filepath.Clean(absA) == filepath.Clean(absB)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
