package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/toolevents"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/prompt"
	"github.com/juex-ai/juex/internal/framework/provenance"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func newStubAgent(t *testing.T, replies ...llm.Response) (*Agent, *stubProvider) {
	t.Helper()
	provider := &stubProvider{replies: replies}
	a, err := newTestAgent(t.TempDir(), provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	return a, provider
}

// Execution fixtures assemble neutral resources and policies; product configuration
// and concrete Module combinations remain covered by App integration tests.
func newTestAgent(dir string, provider llm.Provider) (_ *Agent, resultErr error) {
	ctx, cancel := context.WithCancel(context.Background())
	transferred := false
	defer func() {
		if !transferred {
			cancel()
		}
	}()
	store := thread.NewStore(filepath.Join(dir, ".juex"))
	th, err := store.EnsureMain()
	if err != nil {
		return nil, err
	}
	defer func() {
		if !transferred {
			_ = th.Close()
		}
	}()
	defs := append(runtime.EventDefinitions(), thread.EventDefinitions()...)
	defs = append(defs, provenance.EventDefinitions()...)
	defs = append(defs, toolevents.EventDefinitions()...)
	catalog, err := events.NewCatalog(defs...)
	if err != nil {
		return nil, err
	}
	sink := events.NewDurableSink(th)
	sink.SetCatalog(catalog)
	bus := events.NewBus()
	bus.SetCommitter(sink)
	status, err := runtime.NewStatusStoreFromReplay(RuntimeStatusSeed(th, runtime.DefaultMaxPendingInput), func(visit func(events.Event)) error { th.ReplayEvents(visit); return nil })
	if err != nil {
		return nil, err
	}
	runtimeContext := runtimemodule.RuntimeContext{ID: "test-agent", WorkDir: dir, AgentStateDir: filepath.Join(dir, ".juex"), MediaDir: filepath.Join(dir, ".juex", "media")}
	tools := toolcore.NewRegistry()
	engine := &runtime.Engine{Provider: provider, SummaryProvider: provider, Bus: bus, Thread: th, Tools: tools, RuntimeContext: runtimeContext,
		Prompt: &prompt.Builder{ModulePromptContext: func() ([]runtimemodule.ContextSection, error) { return nil, nil }}, WorkDir: dir, MediaDir: runtimeContext.MediaDir,
		PendingInputQueue: runtime.NewPendingInputQueue(th.Dir, runtime.PendingInputQueueOptions{Thread: th}), PendingInputTTL: runtime.DefaultPendingInputTTL, ExternalEventTTL: runtime.DefaultExternalEventTTL}
	policy := InputPolicy{CommandHelp: "Available commands: /status /new /compact", ParseCommand: func(input string) (Command, bool, error) {
		if !strings.HasPrefix(input, "/") {
			return Command{}, false, nil
		}
		name, args, _ := strings.Cut(input, " ")
		if args != "" {
			return Command{}, true, errors.New("command takes no arguments")
		}
		return Command{Name: name, Kind: CommandKind(strings.TrimPrefix(name, "/"))}, true, nil
	}}
	policy.Status = func() (CommandStatus, error) {
		return CommandStatus{JSON: json.RawMessage(`{}`), Text: "test status", GenerationID: th.Info().GenerationID}, nil
	}
	a, err := New(Options{Engine: engine, Status: status, Bus: bus, Thread: th, ThreadStore: store, Context: ctx, Cancel: cancel, Stderr: io.Discard, EventSink: sink, EventCatalog: catalog, EventUnsubscribe: func() { bus.SetCommitter(nil) }, RuntimeContext: runtimeContext, InputPolicy: policy, MediaDir: runtimeContext.MediaDir})
	if err != nil {
		return nil, err
	}
	transferred = true
	defer func() {
		if resultErr != nil {
			_ = a.CloseAndWait()
		}
	}()
	runtimeModules, err := runtimemodule.BuildAndStartRuntimeSet(ctx, nil, runtimeContext, runtimemodule.ToolContext{Runtime: runtimeContext})
	if err != nil {
		return nil, err
	}
	a.BindRuntimeModules(runtimeModules)
	threadContext := runtimemodule.ThreadContext{ID: th.ID, Dir: th.Dir}
	threadModules, err := runtimemodule.BuildAndStartThreadSet(ctx, nil, threadContext, runtimemodule.ToolContext{Runtime: runtimeContext, Thread: &threadContext})
	if err != nil {
		return nil, err
	}
	if err := engine.ReplaceThreadRuntimeBundle(th, runtime.ThreadRuntimeReplacement{Modules: threadModules, Tools: tools}); err != nil {
		return nil, err
	}
	if err := a.RestoreAndActivate(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

func testExternalInput(id string) llm.Message {
	msg := llm.TextMessage(llm.RoleUser, id)
	msg.ID = id
	msg.Kind = llm.MessageKindObservation
	return msg
}

func deliverTestInput(a *Agent, ctx context.Context, message llm.Message) (ExternalDelivery, error) {
	return a.DeliverExternalInput(ctx, func() (llm.Message, runtime.PendingInputOptions, error) {
		return message, runtime.PendingInputOptions{ID: message.ID, TTL: time.Hour}, nil
	})
}
