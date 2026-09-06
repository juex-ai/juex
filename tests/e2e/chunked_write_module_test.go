package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modulecatalog"
)

type bufferedWriteProvider struct{ chunkedWriteProvider }

func (p *bufferedWriteProvider) Complete(ctx context.Context, system string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	if p.called.Load() == 3 {
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Buffered for a later turn."), StopReason: llm.StopEndTurn}, nil
	}
	return p.chunkedWriteProvider.Complete(ctx, system, history, specs)
}

func TestChunkedWriteModuleRecoversCurrentGenerationOnly(t *testing.T) {
	for _, transition := range []string{"restart", "disable-enable", "new"} {
		t.Run(transition, func(t *testing.T) {
			isolateModuleConfig(t)
			work := t.TempDir()
			modules := config.ModulePolicy{}
			for _, definition := range modulecatalog.Definitions() {
				modules[definition.ID] = config.ModuleSettings{Enabled: definition.ID == modulecatalog.ChunkedWrite}
			}
			cfg := config.Config{WorkDir: work, AgentStateDir: filepath.Join(work, "state"), Modules: modules}
			writer := &bufferedWriteProvider{chunkedWriteProvider: chunkedWriteProvider{t: t, contentA: "first\n", contentB: "second\n"}}
			application, err := app.New(app.Options{Config: cfg, Provider: writer})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := application.Run(t.Context(), "Buffer a report."); err != nil {
				_ = application.CloseAndWait()
				t.Fatal(err)
			}
			writeID := chunkWriteIDFromMessages(t, application.Thread.History)
			if transition == "new" {
				if err := application.NewContext(t.Context()); err != nil {
					_ = application.CloseAndWait()
					t.Fatal(err)
				}
			}
			if err := application.CloseAndWait(); err != nil {
				t.Fatal(err)
			}
			if transition == "disable-enable" {
				modules[modulecatalog.ChunkedWrite] = config.ModuleSettings{Enabled: false}
				application, err = app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}})
				if err != nil {
					t.Fatal(err)
				}
				if err := application.CloseAndWait(); err != nil {
					t.Fatal(err)
				}
				modules[modulecatalog.ChunkedWrite] = config.ModuleSettings{Enabled: true}
			}
			reader := &bareScriptProvider{steps: []llm.Response{
				{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "resume_commit", ToolName: "write_commit", Input: map[string]any{"write_id": writeID, "expected_chunks": 2}}}}, StopReason: llm.StopToolUse},
				{Message: llm.TextMessage(llm.RoleAssistant, "Checked the buffered write."), StopReason: llm.StopEndTurn},
			}}
			application, err = app.New(app.Options{Config: cfg, Provider: reader})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = application.CloseAndWait() }()
			if _, err := application.Run(t.Context(), "Commit the buffered report."); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(work, "reports", "long.md"))
			if transition == "new" {
				if !os.IsNotExist(err) {
					t.Fatalf("new Generation restored old buffer: %q %v", data, err)
				}
				if !strings.Contains(toolResultText(reader.history[len(reader.history)-1]), "unknown write_id") {
					t.Fatal("old write was not rejected")
				}
			} else if err != nil || string(data) != writer.contentA+writer.contentB {
				t.Fatalf("restored commit: %q %v", data, err)
			}
			for _, history := range reader.history {
				if err := llm.ValidateToolTranscript(history); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestChunkedWriteModuleDisabledKeepsHistoricalToolPairsWithoutFolding(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		maxBytes int
		fold     bool
	}{
		{name: "default-budget", fold: true},
		{name: "small-budget", maxBytes: 100},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			isolateModuleConfig(t)
			work := t.TempDir()
			modules := config.ModulePolicy{}
			for _, definition := range modulecatalog.Definitions() {
				modules[definition.ID] = config.ModuleSettings{Enabled: definition.ID == modulecatalog.ChunkedWrite}
			}
			cfg := config.Config{WorkDir: work, AgentStateDir: filepath.Join(work, "state"), Modules: modules, ToolOutput: config.ToolOutputConfig{InlineMaxBytes: testCase.maxBytes}}
			writer := &chunkedWriteProvider{t: t, contentA: strings.Repeat("alpha\n", 80), contentB: strings.Repeat("beta\n", 80)}
			application, err := app.New(app.Options{Config: cfg, Provider: writer})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := application.Run(context.Background(), "Write the report with chunks."); err != nil {
				_ = application.CloseAndWait()
				t.Fatal(err)
			}
			if folded := strings.Contains(messagesText(writer.history[len(writer.history)-1]), "Chunked write provider replay summary: committed"); folded != testCase.fold {
				t.Fatalf("folded=%t, want %t for output budget %d", folded, testCase.fold, testCase.maxBytes)
			}
			journal := filepath.Join(application.Thread.Dir, "generations", "g000001.jsonl")
			if err := application.CloseAndWait(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(journal)
			if err != nil {
				t.Fatal(err)
			}
			modules[modulecatalog.ChunkedWrite] = config.ModuleSettings{Enabled: false}
			reader := &bareScriptProvider{steps: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "History remains readable."), StopReason: llm.StopEndTurn}}}
			application, err = app.New(app.Options{Config: cfg, Provider: reader})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = application.CloseAndWait() }()
			if _, err := application.Run(context.Background(), "Read the existing conversation."); err != nil {
				t.Fatal(err)
			}
			if len(reader.history) != 1 || len(reader.tools[0]) != 0 {
				t.Fatalf("disabled provider requests: histories=%d tools=%v", len(reader.history), reader.tools)
			}
			history := reader.history[0]
			if err := llm.ValidateToolTranscript(history); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(messagesText(history), "Chunked write provider replay summary:") {
				t.Fatal("disabled module still folded chunk history")
			}
			encoded, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			for _, content := range []string{writer.contentA, writer.contentB} {
				encodedContent, _ := json.Marshal(content)
				if !bytes.Contains(encoded, encodedContent) {
					t.Fatal("disabled history lost original chunk arguments")
				}
			}
			after, err := os.ReadFile(journal)
			if err != nil || !bytes.HasPrefix(after, before) {
				t.Fatalf("existing journal facts changed: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(work, "reports", "long.md"))
			if err != nil || string(data) != writer.contentA+writer.contentB {
				t.Fatalf("committed user file changed: %v", err)
			}

		})
	}
}
