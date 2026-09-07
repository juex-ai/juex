// Package scratchpad owns persistent Thread working files and their guidance.
package scratchpad

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/app/modulecatalog"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

const ModuleID runtimemodule.ID = modulecatalog.Scratchpad

type Module struct {
	WorkDir string
	dir     string
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

// Dir locates existing working storage for resource adapters without opening it.
func Dir(threadDir string) string {
	if strings.TrimSpace(threadDir) == "" {
		return ""
	}
	return filepath.Join(threadDir, "scratchpad")
}

func (m *Module) StartThread(_ context.Context, thread runtimemodule.ThreadContext) error {
	dir := Dir(thread.Dir)
	if dir == "" {
		return fmt.Errorf("scratchpad: Thread directory is required")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("scratchpad: prepare working directory: %w", err)
	}
	m.dir = dir
	return nil
}

// Working files belong to the Thread across runtime and Generation changes.
func (*Module) CloseThread(context.Context) error { return nil }

func (m *Module) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	section, ok := scratchpadSection(m.WorkDir, m.dir)
	if !ok {
		return nil, nil
	}
	section.Projection = runtimemodule.ContextProjectionSystemPrompt
	section.Budget = runtimemodule.UnboundedContextBudget()
	return []runtimemodule.ContextSection{section}, nil
}

func scratchpadSection(workDir, scratchpadDir string) (runtimemodule.ContextSection, bool) {
	dir := strings.TrimSpace(scratchpadDir)
	if dir == "" {
		return runtimemodule.ContextSection{}, false
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	lines := []string{"## Thread Scratchpad", fmt.Sprintf("- path: %s", dir)}
	if rel, ok := scratchpadRelativePath(workDir, dir); ok {
		lines = append(lines, fmt.Sprintf("- workspace-relative path: %s", rel))
	}
	lines = append(lines,
		"- Use this directory for long drafts, intermediate files, and working material.",
		"- Scratchpad contents are not automatically added to context. Retrieve needed contents with the available file tools.",
		"- Save important intermediate conclusions here before compaction so a later turn can read them back.",
	)
	return runtimemodule.ContextSection{
		Key: "thread_scratchpad", Label: "Thread Scratchpad", Source: "runtime", Path: dir, Text: strings.Join(lines, "\n"),
	}, true
}

func scratchpadRelativePath(workDir, dir string) (string, bool) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return "", false
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
