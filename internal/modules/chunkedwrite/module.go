// Package chunkedwrite owns Thread buffered writes, recovery and history folding.
package chunkedwrite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	writefacts "github.com/juex-ai/juex/internal/chunkedwrite"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modulecatalog"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/sandbox"
	"github.com/juex-ai/juex/internal/tools"
)

const ModuleID runtimemodule.ID = modulecatalog.ChunkedWrite

type Module struct {
	options tools.BuiltinOptions
	manager *tools.ChunkedWriteManager
}

func New(options tools.BuiltinOptions) *Module { return &Module{options: options} }
func (*Module) ID() runtimemodule.ID           { return ModuleID }

func (m *Module) StartThread(context.Context, runtimemodule.ThreadContext) error {
	if m.manager != nil {
		return fmt.Errorf("chunked-write: Thread already started")
	}
	guard := sandbox.NewFilePolicy(sandbox.FilePolicyOptions{Policy: m.options.Sandbox, WorkDir: m.options.WorkDir, AgentStateDir: m.options.AgentStateDir, ReadOnlyPaths: []string{m.options.MediaDir}})
	m.manager = tools.NewChunkedWriteManager(m.options.WorkDir, guard)
	return nil
}

func (m *Module) CloseThread(context.Context) error { return m.manager.Close() }

// Buffered sessions belong to the committed Generation's tool history.
func (m *Module) ContextRenewed(context.Context) { m.manager.RestoreActiveSessions(nil) }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	if m.manager == nil {
		return nil, fmt.Errorf("chunked-write: Thread has not started")
	}
	options := m.options
	options.Providers = []tools.BuiltinProvider{tools.ChunkedWriteToolProvider{}}
	options.ChunkedWrites = m.manager
	contributions := tools.BuiltinTools(options)
	for i := range contributions {
		handler := contributions[i].ResultHandler
		contributions[i].ResultHandler = func(ctx context.Context, input map[string]any) (tools.Result, error) {
			result, err := handler(ctx, input)
			if event, ok := writefacts.EventFromStructured(result.Structured); ok {
				result.Fact, _ = json.Marshal(event)
			}
			return result, err
		}
	}
	return contributions, nil
}

func (m *Module) ApplyThreadStart(ctx context.Context, request runtimemodule.ThreadStartRequest) (runtimemodule.ThreadStartDecision, error) {
	if err := ctx.Err(); err != nil {
		return runtimemodule.ThreadStartDecision{}, err
	}
	if m.manager == nil {
		return runtimemodule.ThreadStartDecision{}, fmt.Errorf("chunked-write: Thread has not started")
	}
	m.manager.RestoreActiveSessions(recoverActiveSessions(request.History))
	return runtimemodule.ThreadStartDecision{}, ctx.Err()
}

func eventFromResult(block llm.Block) *writefacts.Event {
	if block.ResultFact == nil || block.ResultFact.Owner != string(ModuleID) || len(block.ResultFact.Data) == 0 {
		return nil
	}
	var event writefacts.Event
	if err := json.Unmarshal(block.ResultFact.Data, &event); err != nil || event.WriteID == "" {
		return nil
	}
	switch event.Kind {
	case writefacts.EventBegin, writefacts.EventChunk, writefacts.EventCommit, writefacts.EventAbort:
		return &event
	default:
		return nil
	}
}

func recoverActiveSessions(history []llm.Message) []tools.ChunkedWriteRecoverySession {
	uses := map[string][]llm.Block{}
	sessions := map[string]tools.ChunkedWriteRecoverySession{}
	invalid := map[string]bool{}
	for _, message := range history {
		for _, result := range message.Blocks {
			if result.Type == llm.BlockToolUse {
				uses[result.ToolUseID] = append(uses[result.ToolUseID], result)
				continue
			}
			if result.Type != llm.BlockToolResult {
				continue
			}
			pending := uses[result.ToolUseID]
			if len(pending) == 0 {
				continue
			}
			use := pending[0]
			uses[result.ToolUseID] = pending[1:]
			event := eventFromResult(result)
			if event == nil {
				continue
			}
			switch event.Kind {
			case writefacts.EventBegin:
				sessions[event.WriteID] = tools.ChunkedWriteRecoverySession{WriteID: event.WriteID, Path: event.Path, Mode: event.Mode, FileMode: event.FileMode}
				delete(invalid, event.WriteID)
			case writefacts.EventChunk:
				session, active := sessions[event.WriteID]
				if !active || invalid[event.WriteID] {
					continue
				}
				content, ok := use.Input["content"].(string)
				hash := sha256.Sum256([]byte(content))
				if !ok || event.Index < 0 || (event.SHA256 != "" && !strings.EqualFold(event.SHA256, hex.EncodeToString(hash[:]))) {
					invalid[event.WriteID] = true
					continue
				}
				chunk := tools.ChunkedWriteRecoveryChunk{Index: event.Index, Content: content}
				replaced := false
				for i := range session.Chunks {
					if session.Chunks[i].Index == event.Index {
						session.Chunks[i], replaced = chunk, true
						break
					}
				}
				if !replaced {
					session.Chunks = append(session.Chunks, chunk)
				}
				sessions[event.WriteID] = session
			case writefacts.EventCommit, writefacts.EventAbort:
				delete(sessions, event.WriteID)
				delete(invalid, event.WriteID)
			}
		}
	}
	out := make([]tools.ChunkedWriteRecoverySession, 0, len(sessions))
	for id, session := range sessions {
		if !invalid[id] {
			out = append(out, session)
		}
	}
	return out
}
