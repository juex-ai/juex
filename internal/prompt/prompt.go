// Package prompt assembles Framework-validated Module context into the system
// prompt that opens every turn.
package prompt

import (
	"fmt"
	"strings"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

const SectionSeparator = "\n\n---\n\n"

type Builder struct {
	ModulePromptContext func() ([]runtimemodule.ContextSection, error)
}

type Section = runtimemodule.ContextSection

func (b *Builder) BuildWithError() (string, error) {
	sections, err := b.SectionsWithError()
	if err != nil {
		return "", err
	}
	return JoinSections(sections), nil
}

func (b *Builder) SectionsWithError() ([]Section, error) {
	if b == nil || b.ModulePromptContext == nil {
		return nil, nil
	}
	moduleSections, err := b.ModulePromptContext()
	if err != nil {
		return nil, fmt.Errorf("prompt: module context: %w", err)
	}
	return runtimemodule.SectionsForProjection(moduleSections, runtimemodule.ContextProjectionSystemPrompt), nil
}

func JoinSections(sections []Section) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		if section.Text != "" {
			parts = append(parts, section.Text)
		}
	}
	return strings.Join(parts, SectionSeparator)
}
