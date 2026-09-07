// Package operatingcontext contributes concise execution environment context.
package operatingcontext

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

const ModuleID = "operating-context"

type Module struct {
	WorkDir string
	Now     func() time.Time
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	return []runtimemodule.ContextSection{{
		Key: "operating_context", Label: "Operating Context", Source: "runtime",
		Text: operatingContext(m.WorkDir, m.Now), Projection: runtimemodule.ContextProjectionSystemPrompt,
		Budget: runtimemodule.UnboundedContextBudget(),
	}}, nil
}

func operatingContext(workDir string, nowFn func() time.Time) string {
	now := time.Now
	if nowFn != nil {
		now = nowFn
	}
	cwd := workDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	} else if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	lines := []string{
		"## Operating Context",
		fmt.Sprintf("- cwd: %s", cwd),
		fmt.Sprintf("- os: %s/%s", runtime.GOOS, runtime.GOARCH),
		fmt.Sprintf("- time: %s", now().UTC().Format(time.RFC3339)),
	}
	return strings.Join(lines, "\n")
}
