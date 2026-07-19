// Package app wires process-level runtime dependencies: config -> provider ->
// registry -> tools -> MCP -> skills -> memory -> session -> prompt -> engine.
//
// It also owns application policies shared by transports, such as workspace
// session attachment, slash commands, MCP notification routing, and turn
// admission. CLI and web code may still import lower-level packages for their
// own presentation and inspection surfaces; shared runtime decisions should
// move here instead of being duplicated across transports.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/eventmedia"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/observability"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/sandbox"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
	"github.com/juex-ai/juex/internal/usermedia"
)

// Options bundles the inputs to New.
type Options struct {
	Config   config.Config
	Provider llm.Provider // optional; if nil, derived from Config
	// ModelCandidates takes precedence over Provider and config-derived models.
	// ModelHealth may be shared by multiple Apps, as juex serve does.
	ModelCandidates []runtime.ModelCandidate
	ModelHealth     *llm.ModelHealth
	// SummaryProvider, when set, overrides compaction.summary_model provider
	// construction. It is primarily useful for tests and embedded callers.
	SummaryProvider llm.Provider
	Verbose         bool
	Debug           bool
	LogLevel        string
	Stderr          io.Writer
	WorkDir         string // if set, overrides Config.WorkDir
	// MCPManager, when set, provides process-scoped MCP clients owned by
	// the caller. App registers proxy tools into its per-session registry
	// but does not close the manager.
	MCPManager *mcp.Manager
	// DisableMCP skips loading MCP configs. Used by serve when MCP startup
	// failed at process scope but sessions should still be usable.
	DisableMCP bool
	// SuppressMCPWarnings keeps optional MCP startup diagnostics out of stderr.
	// Callers that expose structured diagnostics, such as dry-run JSON, set this.
	SuppressMCPWarnings bool
	// ResumeDir, if non-empty, is the absolute path of an existing
	// session directory to load instead of creating a new one. The
	// session ID and on-disk files are reused; new messages append.
	ResumeDir   string
	Alias       string
	SessionMode SessionMode
	// LazySession delays creating the on-disk session directory until the
	// first message or event is appended. Used by the web UI so abandoned
	// empty chats do not leave local files behind.
	LazySession bool
}

type SessionMode string

const (
	SessionModeAttachActive SessionMode = "attach_active"
	SessionModeNewPrimary   SessionMode = "new_primary"
	SessionModeNewSide      SessionMode = "new_side"
)

type App struct {
	Engine         *runtime.Engine
	Status         *runtime.StatusStore
	Bus            *events.Bus
	Session        *session.Session
	cleanup        []func() error
	closeMu        sync.Mutex
	closeCancel    sync.Once
	cleanupIndex   int
	closeErr       error
	closeRunning   bool
	closeRunDone   chan struct{}
	closeRunResult *error
	sessionMu      sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	cfg            config.Config
	stderr         io.Writer
	skills         []skills.Skill
	mcp            MCPStatus
	obsv           *observable.Manager
	chunkedWrites  *tools.ChunkedWriteManager

	turnAdmission turnAdmission

	sessionLock       *session.Lock
	eventSink         *events.DurableSink
	eventUnsubscribe  func()
	statusUnsubscribe func()

	debug                    bool
	logLevel                 string
	recorder                 *observability.Recorder
	observabilityUnsubscribe func()
}

type MCPStatus struct {
	Configured int               `json:"configured"`
	Connected  int               `json:"connected"`
	Errors     int               `json:"errors"`
	Servers    []MCPServerStatus `json:"servers"`
}

// CloseDeferredError reports that another App cleanup pass is in progress.
// Callback callers must return before waiting on it.
type CloseDeferredError struct {
	done   <-chan struct{}
	result *error
}

func (*CloseDeferredError) Error() string {
	return "app: close deferred while cleanup is in progress"
}

func (e *CloseDeferredError) Wait() error {
	if e == nil || e.done == nil {
		return nil
	}
	<-e.done
	if e.result == nil {
		return nil
	}
	return *e.result
}

type MCPServerStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Connected bool   `json:"connected"`
	ToolCount int    `json:"tool_count"`
	Error     string `json:"error,omitempty"`
}

// New wires every subsystem and returns a ready-to-use App.
// The caller must Close() to flush jsonl and stop MCP subprocesses.
func New(opts Options) (*App, error) {
	cfg := opts.Config
	if opts.WorkDir != "" {
		cfg.WorkDir = opts.WorkDir
	}
	if cfg.WorkDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("app: workdir: %w", err)
		}
		cfg.WorkDir = wd
	}
	runtimePaths := cfg.RuntimePaths()
	resourcePaths := cfg.ResourcePaths()
	runtimeLimits := cfg.RuntimeLimits()
	resourceGraph, err := ResolveRuntimeResourceGraph(cfg)
	if err != nil {
		return nil, err
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	provider := opts.Provider
	modelCandidates := append([]runtime.ModelCandidate(nil), opts.ModelCandidates...)
	providerInjected := provider != nil || len(modelCandidates) > 0
	switch {
	case len(modelCandidates) > 0:
		provider = modelCandidates[0].Provider
	case provider != nil:
		// A single injected provider is a compatibility/test seam and
		// intentionally disables config-derived fallbacks.
	default:
		resolvedChain, err := cfg.ModelChain()
		if err != nil {
			return nil, err
		}
		modelCandidates = make([]runtime.ModelCandidate, 0, len(resolvedChain))
		for _, resolved := range resolvedChain {
			profile, err := resolved.Selection.ProviderProfile()
			if err != nil {
				return nil, err
			}
			candidateProvider, err := llm.NewProvider(profile)
			if err != nil {
				return nil, err
			}
			modelCandidates = append(modelCandidates, runtime.ModelCandidate{
				Ref:             resolved.Ref,
				Provider:        candidateProvider,
				ContextWindow:   resolved.ContextWindow,
				MaxOutputTokens: resolved.MaxOutputTokens,
			})
		}
		provider = modelCandidates[0].Provider
	}
	modelHealth := opts.ModelHealth
	if modelHealth == nil {
		modelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	}
	summaryProvider := opts.SummaryProvider
	if summaryProvider == nil && !providerInjected && strings.TrimSpace(runtimeLimits.Compaction.SummaryModel) != "" {
		profile, err := cfg.ProviderProfileForModelRef(runtimeLimits.Compaction.SummaryModel)
		if err != nil {
			return nil, fmt.Errorf("app: compaction.summary_model: %w", err)
		}
		p, err := llm.NewProvider(profile)
		if err != nil {
			return nil, fmt.Errorf("app: compaction.summary_model: %w", err)
		}
		summaryProvider = p
	}

	bus := events.NewBus()
	if opts.Verbose {
		vp := newVerbosePrinter(stderr)
		bus.Subscribe("*", vp.handle)
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	shellSessions := tools.NewShellSessionManager(appCtx)
	appContextTransferred := false
	defer func() {
		if !appContextTransferred {
			_ = shellSessions.Close()
			appCancel()
		}
	}()
	toolTimeoutSeconds := durationSeconds(runtimeLimits.ToolTimeout)
	reg := tools.NewRegistryWithOptions(tools.RegistryOptions{
		DefaultTimeoutSeconds: toolTimeoutSeconds,
	})
	chunkedWrites := tools.NewChunkedWriteManager(runtimePaths.WorkDir, sandbox.NewPathGuard(runtimePaths.WorkDir, cfg.SandboxPolicy()))
	tools.RegisterBuiltins(reg, tools.BuiltinOptions{
		WorkDir:            runtimePaths.WorkDir,
		Shell:              toolsShellProfile(cfg.Shell),
		ShellSessions:      shellSessions,
		Sandbox:            cfg.SandboxPolicy(),
		ToolTimeoutSeconds: toolTimeoutSeconds,
		ChunkedWrites:      chunkedWrites,
	})

	skillLoader := skills.NewLoaderFromDirsWithOptions(resourceGraph.SkillDirs(), skillLoaderOptions(cfg))
	if err := skillLoader.Load(); err != nil {
		return nil, err
	}
	if err := registerSkillTools(reg, skillLoader, runtimePaths.WorkDir, cfg.SandboxPolicy()); err != nil {
		return nil, err
	}

	memStore := memory.NewStore(runtimePaths.MemoryDir)
	if err := memStore.RegisterTools(reg); err != nil {
		return nil, err
	}

	var mcpConfigs []mcp.Config
	var mergedMCP mcp.Config
	if !opts.DisableMCP && opts.MCPManager == nil {
		var err error
		mcpConfigs, mergedMCP, _, err = loadMCPConfigRefs(resourceGraph.MCPConfigs(), runtimePaths.WorkDir)
		if err != nil {
			return nil, err
		}
	}

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{
		ResumeDir: opts.ResumeDir,
		Mode:      opts.SessionMode,
		Alias:     opts.Alias,
		Lazy:      opts.LazySession,
	})
	if err != nil {
		return nil, err
	}
	sess := attachment.Session
	sessLock, err := session.AcquireSessionLock(sess.Dir, attachment.LockMode)
	if err != nil {
		sess.Close()
		return nil, err
	}
	chunkedWrites.RestoreActiveFromHistory(sess.History)
	var eventSink *events.DurableSink
	var eventUnsubscribe func()
	var statusUnsubscribe func()
	closeSessionResources := func() {
		if statusUnsubscribe != nil {
			statusUnsubscribe()
			statusUnsubscribe = nil
		}
		if eventUnsubscribe != nil {
			eventUnsubscribe()
			eventUnsubscribe = nil
		}
		if eventSink != nil {
			eventSink.Close()
			eventSink = nil
		}
		_ = sessLock.Close()
		_ = sess.Close()
	}
	eventSink = events.NewDurableSink(sess)
	eventUnsubscribe = bus.Subscribe("*", eventSink.Handle)
	journalEvents, statusReplayErr := session.ReadEvents(sess.Dir)
	if statusReplayErr != nil {
		fmt.Fprintf(stderr, "juex: warning: restore runtime status: %v; continuing with recovered events\n", statusReplayErr)
	}
	status := runtime.NewStatusStore(runtimeStatusSeed(sess, runtime.DefaultMaxPendingInput))
	status.Reset(runtimeStatusSeed(sess, runtime.DefaultMaxPendingInput), journalEvents)
	status.RecoverAfterRestart()
	statusUnsubscribe = eventSink.AddProjection(status)

	pb := &prompt.Builder{
		GlobalAgentsMDPath: resourcePaths.GlobalAgentsMDPath,
		AgentsMDDirs:       resourcePaths.AgentsMDDirs,
		Memory:             memStore,
		Skills:             skillLoader,
		ScratchpadDir:      sess.ScratchpadDir(),
		WorkDir:            runtimePaths.WorkDir,
		Shell:              prompt.ShellProfileFromConfig(cfg.Shell),
		RuntimeSections: func() []prompt.Section {
			text := tools.FormatActiveShellSessionsPrompt(shellSessions.List(false))
			if text == "" {
				return nil
			}
			return []prompt.Section{{
				Key:    "active_shell_sessions",
				Label:  "Active Shell Sessions",
				Source: "runtime",
				Text:   text,
			}}
		},
	}
	hookRunner, err := hooks.NewRunner(resourceGraph.HooksConfig())
	if err != nil {
		closeSessionResources()
		return nil, err
	}
	pendingInputTTL := runtimeLimits.PendingInputTTL
	if pendingInputTTL <= 0 {
		pendingInputTTL = runtime.DefaultPendingInputTTL
	}
	externalEventTTL := runtimeLimits.ExternalEventTTL
	if externalEventTTL <= 0 {
		externalEventTTL = runtime.DefaultExternalEventTTL
	}

	eng := &runtime.Engine{
		Provider:        provider,
		SummaryProvider: summaryProvider,
		ModelCandidates: modelCandidates,
		ModelHealth:     modelHealth,
		Tools:           reg,
		Bus:             bus,
		Session:         sess,
		Prompt:          pb,
		WorkDir:         runtimePaths.WorkDir,
		Hooks:           hookRunner,
		HookContext: hooks.Request{
			CWD:              runtimePaths.WorkDir,
			WorkspaceRoots:   []string{runtimePaths.WorkDir},
			PermissionMode:   "unrestricted",
			SandboxMode:      "none",
			ConversationPath: filepath.Join(sess.Dir, "conversation.jsonl"),
			EventsPath:       filepath.Join(sess.Dir, "events.jsonl"),
		},
		PendingInputQueue:     runtime.NewPendingInputQueue(sess.Dir, runtime.PendingInputQueueOptions{}),
		Notes:                 notesStore(sess),
		PendingInputTTL:       pendingInputTTL,
		ExternalEventTTL:      externalEventTTL,
		GoalState:             goalStateStore(sess),
		ShowBuiltinHookTraces: runtimeLimits.ShowBuiltinHookTraces,
		ContextWindow:         runtimeLimits.ContextWindow,
		MaxOutputTokens:       runtimeLimits.MaxOutputTokens,
		Compaction:            runtimeLimits.Compaction,
	}
	if err := eng.ReplaceSessionRuntime(sess); err != nil {
		closeSessionResources()
		return nil, err
	}

	a := &App{
		Engine:            eng,
		Status:            status,
		Bus:               bus,
		Session:           sess,
		ctx:               appCtx,
		cancel:            appCancel,
		cfg:               cfg,
		stderr:            stderr,
		skills:            skillLoader.All(),
		chunkedWrites:     chunkedWrites,
		sessionLock:       sessLock,
		eventSink:         eventSink,
		eventUnsubscribe:  eventUnsubscribe,
		statusUnsubscribe: statusUnsubscribe,
		debug:             opts.Debug,
		logLevel:          opts.LogLevel,
	}
	if err := runtime.RegisterGoalTools(reg, eng); err != nil {
		_ = a.detachObservability()
		closeSessionResources()
		return nil, err
	}
	if err := runtime.RegisterNotesTools(reg, eng); err != nil {
		_ = a.detachObservability()
		closeSessionResources()
		return nil, err
	}
	if err := a.attachObservability(sess); err != nil {
		closeSessionResources()
		return nil, err
	}
	obsv, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath:    cfg.ObservablesConfigPath(),
		StateDir:      cfg.ObservablesStateDir(),
		WorkDir:       runtimePaths.WorkDir,
		Shell:         cfg.Shell,
		Sandbox:       cfg.SandboxPolicy(),
		SandboxRunner: nil,
		Bus:           bus,
		Deliver:       a.DeliverObservation,
	})
	if err != nil {
		_ = a.detachObservability()
		closeSessionResources()
		return nil, err
	}
	a.obsv = obsv
	if err := observable.RegisterTools(reg, obsv); err != nil {
		_ = obsv.Close()
		_ = a.detachObservability()
		closeSessionResources()
		return nil, err
	}
	a.mcp = buildMCPStatus(mergedMCP.MCPServers, nil, nil)
	a.cleanup = append(a.cleanup, obsv.Close, shellSessions.Close, func() error {
		if err := a.detachObservability(); err != nil {
			return err
		}
		return nil
	}, func() error {
		if a.statusUnsubscribe != nil {
			a.statusUnsubscribe()
			a.statusUnsubscribe = nil
		}
		if a.eventUnsubscribe != nil {
			a.eventUnsubscribe()
			a.eventUnsubscribe = nil
		}
		if a.eventSink != nil {
			a.eventSink.Close()
		}
		return nil
	}, a.closeActiveSessionResources)
	if opts.MCPManager != nil {
		if err := opts.MCPManager.RegisterTools(reg); err != nil {
			_ = a.detachObservability()
			closeSessionResources()
			return nil, err
		}
		a.mcp = buildMCPStatus(nil, opts.MCPManager.ToolCounts(), opts.MCPManager.StartupErrors())
	} else if len(mcpConfigs) > 0 {
		connectOpts := mcp.ConnectOptions{
			Stderr:        stderr,
			ForwardStderr: opts.Verbose,
		}
		if sess.Kind == session.KindPrimary {
			connectOpts.EnableClaudeChannel = true
			connectOpts.OnNotification = func(n mcp.Notification) {
				_ = a.HandleMCPNotification(a.ctx, n)
			}
		}
		mgr, err := mcp.NewManagerLayeredSoft(context.Background(), mcpConfigs, connectOpts)
		if err != nil {
			_ = a.detachObservability()
			closeSessionResources()
			return nil, err
		}
		startupErrors := mgr.StartupErrors()
		if !opts.SuppressMCPWarnings {
			writeMCPStartupWarnings(stderr, startupErrors)
		}
		if err := mgr.RegisterTools(reg); err != nil {
			if closeErr := mgr.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
			_ = a.detachObservability()
			closeSessionResources()
			return nil, err
		}
		a.mcp = buildMCPStatus(mergedMCP.MCPServers, mgr.ToolCounts(), startupErrors)
		a.cleanup = append(a.cleanup, mgr.Close)
	}
	if err := eng.RunSessionStartHooks(appCtx); err != nil {
		_ = a.Close()
		return nil, err
	}
	if sess.Kind == session.KindPrimary && obsv != nil {
		_ = obsv.StartAll(appCtx)
	}
	appContextTransferred = true
	return a, nil
}

func goalStateStore(sess *session.Session) *runtime.GoalStateStore {
	if sess == nil || sess.Dir == "" {
		return nil
	}
	return runtime.NewGoalStateStore(sess.Dir, runtime.GoalStateOptions{})
}

func notesStore(sess *session.Session) *runtime.NotesStore {
	if sess == nil || sess.Dir == "" {
		return nil
	}
	return runtime.NewNotesStore(sess.Dir)
}

func toolsShellProfile(p config.ShellProfile) tools.ShellProfile {
	return tools.ShellProfile{
		Profile:       p.Profile,
		Family:        p.Family,
		Binary:        p.Binary,
		Args:          append([]string(nil), p.Args...),
		PathStyle:     p.PathStyle,
		HostPathStyle: p.HostPathStyle,
	}
}

func (a *App) SwitchToNewPrimarySession() error {
	var oldInfo session.Info
	err := a.ReadSession(func(sess *session.Session) error {
		oldInfo = sess.Info(time.Now().UTC())
		return nil
	})
	if err != nil {
		return fmt.Errorf("app: nil session")
	}
	if oldInfo.Kind == session.KindSide {
		return fmt.Errorf("side sessions cannot switch workspace active session")
	}
	attachment, err := AttachWorkspaceSession(a.cfg, SessionAttachmentRequest{Mode: SessionModeNewPrimary})
	if err != nil {
		return err
	}
	sess := attachment.Session
	sessLock, err := session.AcquireSessionLock(sess.Dir, attachment.LockMode)
	if err != nil {
		_ = sess.Close()
		return err
	}
	if err := a.replaceSession(sess, sessLock); err != nil {
		_ = sessLock.Close()
		_ = sess.Close()
		cleanupErr := session.Delete(a.cfg.SessionsDir(), a.cfg.HistoryPath(), sess.ID)
		restoreErr := session.SetActive(a.cfg.HistoryPath(), oldInfo)
		return errors.Join(err, cleanupErr, restoreErr)
	}
	return nil
}

func (a *App) replaceSession(sess *session.Session, sessLock *session.Lock) error {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	if a.Engine != nil {
		if err := a.Engine.ReplaceSessionRuntime(sess); err != nil {
			return err
		}
	}
	_ = a.detachObservability()
	oldLock := a.sessionLock
	oldSession := a.Session

	a.Session = sess
	a.sessionLock = sessLock
	if a.chunkedWrites != nil {
		a.chunkedWrites.RestoreActiveFromHistory(sess.History)
	}
	if a.eventSink != nil {
		a.eventSink.SetJournal(sess)
	}
	if a.Status != nil {
		journalEvents, err := session.ReadEvents(sess.Dir)
		a.Status.Reset(runtimeStatusSeed(sess, runtime.DefaultMaxPendingInput), journalEvents)
		a.Status.RecoverAfterRestart()
		if err != nil {
			fmt.Fprintf(a.stderr, "juex: warning: restore runtime status: %v; continuing with recovered events\n", err)
		}
	}
	if err := a.attachObservability(sess); err != nil {
		// Session switching happens after startup. Surface recorder failures as
		// a runtime event so callers still receive a usable session.
		a.Bus.Emit(events.Event{Type: "turn.errored", Payload: runtime.TurnErroredPayload{Error: err.Error()}})
	}
	if oldLock != nil {
		_ = oldLock.Close()
	}
	if oldSession != nil {
		_ = oldSession.Close()
	}
	return nil
}

func (a *App) closeActiveSessionResources() error {
	if a == nil {
		return nil
	}
	a.sessionMu.Lock()
	sessLock := a.sessionLock
	sess := a.Session
	a.sessionLock = nil
	a.Session = nil
	a.sessionMu.Unlock()

	var lockErr, sessionErr error
	if sessLock != nil {
		lockErr = sessLock.Close()
	}
	if sess != nil {
		sessionErr = sess.Close()
	}
	return errors.Join(lockErr, sessionErr)
}

func runtimeStatusSeed(sess *session.Session, maxPendingInputs int) runtime.StatusSeed {
	if sess == nil {
		return runtime.StatusSeed{MaxPendingInputs: maxPendingInputs}
	}
	return runtime.StatusSeed{
		SessionID:        sess.ID,
		SessionAlias:     sess.Alias,
		MaxPendingInputs: maxPendingInputs,
		TokenUsage:       sess.TokenUsageSnapshot(),
		ContextUsage:     sess.ContextUsageSnapshot(),
	}
}

func (a *App) AddEventDelivery(delivery events.Delivery) func() {
	if a == nil || a.eventSink == nil {
		return func() {}
	}
	return a.eventSink.AddDelivery(delivery)
}

func (a *App) attachObservability(sess *session.Session) error {
	if a == nil || a.Bus == nil || sess == nil {
		return nil
	}
	rec, err := observability.NewRecorder(observability.Options{
		SessionID:  sess.ID,
		SessionDir: sess.Dir,
		Debug:      a.debug,
		LogLevel:   a.logLevel,
	})
	if err != nil {
		return err
	}
	a.recorder = rec
	a.observabilityUnsubscribe = a.Bus.Subscribe("*", func(e events.Event) {
		_ = rec.Record(e)
	})
	return nil
}

func (a *App) detachObservability() error {
	if a == nil {
		return nil
	}
	if a.observabilityUnsubscribe != nil {
		a.observabilityUnsubscribe()
		a.observabilityUnsubscribe = nil
	}
	if a.recorder == nil {
		return nil
	}
	err := a.recorder.Close()
	a.recorder = nil
	return err
}

// Run drives a single turn synchronously.
func (a *App) Run(ctx context.Context, prompt string) (string, error) {
	if cmd, handled, err := ParseSlashCommand(prompt); handled || err != nil {
		if err != nil {
			return "", err
		}
		if cmd.Name == SlashGoal {
			return a.runEngineTurn(ctx, GoalInstructionPrompt(cmd.Args))
		}
		result, err := a.ExecuteParsedSlashCommand(ctx, cmd)
		if err != nil {
			return "", err
		}
		if cmd.Name == SlashNew {
			return a.runEngineTurnMessage(ctx, NewSessionGreetingMessage())
		}
		return result.Text, nil
	}
	return a.runEngineTurn(ctx, prompt)
}

// RunWithAttachments drives one synchronous text, image, or mixed-content
// user turn. Attachment references must belong to the current session.
func (a *App) RunWithAttachments(ctx context.Context, prompt string, attachments []llm.MediaRef) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: attachment turn requires an initialized session and engine")
	}
	if len(attachments) == 0 {
		return a.Run(ctx, prompt)
	}
	if _, handled, err := ParseSlashCommand(prompt); handled || err != nil {
		if err != nil {
			return "", err
		}
		return "", errors.New("slash commands cannot include attachments")
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.Session == nil {
		return "", errors.New("app: attachment turn requires an initialized session and engine")
	}
	if err := usermedia.ValidateSessionMediaRefs(a.cfg.WorkDir, a.Session.ID, attachments, usermedia.Limits{}); err != nil {
		return "", err
	}
	return a.Engine.TurnMessage(ctx, userTurnMessage(prompt, attachments))
}

func (a *App) runEngineTurn(ctx context.Context, input string) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: turn requires an initialized engine")
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.Engine.Turn(ctx, input)
}

func (a *App) runEngineTurnMessage(ctx context.Context, message llm.Message) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: turn requires an initialized engine")
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.Engine.TurnMessage(ctx, message)
}

func (a *App) CompactWithInstructions(ctx context.Context, reason string, auto bool, instructions string) (runtime.CompactionResult, error) {
	if a == nil || a.Engine == nil {
		return runtime.CompactionResult{}, fmt.Errorf("app: nil engine")
	}
	admitted := events.Normalize(events.Event{Type: runtime.TurnAdmittedType, Payload: runtime.TurnAdmittedPayload{
		NonInterruptible: true,
	}})
	turnID := "compact-" + admitted.ID
	admitted.TurnID = turnID
	a.Bus.Emit(admitted)
	return a.compactWithTurnID(ctx, turnID, reason, auto, instructions)
}

func (a *App) CompactAdmittedWithInstructions(ctx context.Context, turnID, reason string, auto bool, instructions string) (runtime.CompactionResult, error) {
	if a == nil || a.Engine == nil {
		return runtime.CompactionResult{}, fmt.Errorf("app: nil engine")
	}
	if turnID == "" {
		return runtime.CompactionResult{}, fmt.Errorf("app: empty compact turn id")
	}
	return a.compactWithTurnID(ctx, turnID, reason, auto, instructions)
}

func (a *App) compactWithTurnID(ctx context.Context, turnID, reason string, auto bool, instructions string) (runtime.CompactionResult, error) {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	sections := a.Engine.PromptSections()
	systemPrompt := prompt.JoinSections(sections)
	result, err := a.Engine.CompactWithInstructions(ctx, turnID, systemPrompt, reason, auto, instructions)
	if err != nil {
		a.Bus.Emit(events.Event{Type: "turn.errored", TurnID: turnID, Payload: runtime.NewTurnErroredPayload(err)})
		return result, err
	}
	a.Bus.Emit(events.Event{Type: "turn.completed", TurnID: turnID, Payload: runtime.TurnCompletedPayload{
		TokenUsage: a.Session.TokenUsageSnapshot(),
	}})
	return result, nil
}

func (a *App) HandleMCPNotification(ctx context.Context, n mcp.Notification) error {
	if a == nil || a.Engine == nil {
		return nil
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	eventType := n.EventType
	if eventType == "" {
		eventType = "notification"
	}
	msg, err := mcpNotificationMessage(n, eventType, attachmentOptions{WorkDir: a.cfg.WorkDir})
	if err != nil {
		return err
	}
	if _, err := a.Engine.EnqueuePendingMessageWithOptions(ctx, msg, runtime.PendingInputOptions{
		ID:  mcpNotificationPendingInputID(n, eventType),
		TTL: a.Engine.ExternalEventTTL,
	}); err == nil {
		return nil
	} else if !errors.Is(err, runtime.ErrNoActiveTurn) {
		return err
	}
	_, err = a.Engine.TurnMessage(ctx, msg)
	return err
}

func (a *App) HandleObservation(ctx context.Context, record observable.ObservationRecord) error {
	_, err := a.DeliverObservation(ctx, record)
	return err
}

func (a *App) DeliverObservation(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
	if a == nil || a.Engine == nil {
		return observable.DeliveryOutcome{}, nil
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	targetSession := ""
	if a.Session != nil {
		targetSession = a.Session.ID
	}
	select {
	case <-ctx.Done():
		return observable.DeliveryOutcome{}, ctx.Err()
	default:
	}
	msg, attachmentErrors, err := buildObservationMessage(record, attachmentOptions{WorkDir: a.cfg.WorkDir})
	if err != nil {
		return observable.DeliveryOutcome{}, err
	}
	if len(attachmentErrors) > 0 {
		a.markObservationAttachmentError(record, attachmentErrors)
	}
	pendingID := observationPendingInputID(record)
	if _, err := a.Engine.EnqueuePendingMessageWithOptions(ctx, msg, runtime.PendingInputOptions{
		ID:  pendingID,
		TTL: a.Engine.ExternalEventTTL,
	}); err == nil {
		return observable.DeliveryOutcome{
			State:          observable.ObservationStateQueued,
			PendingInputID: pendingID,
			TargetSession:  targetSession,
		}, nil
	} else if !errors.Is(err, runtime.ErrNoActiveTurn) {
		return observable.DeliveryOutcome{}, err
	}
	_, err = a.Engine.TurnMessage(ctx, msg)
	if err == nil {
		return observable.DeliveryOutcome{
			State:         observable.ObservationStateDelivered,
			TargetSession: targetSession,
		}, nil
	}
	return observable.DeliveryOutcome{}, err
}

type attachmentOptions struct {
	WorkDir string
}

func observationMessage(record observable.ObservationRecord, opts attachmentOptions) (llm.Message, error) {
	msg, _, err := buildObservationMessage(record, opts)
	return msg, err
}

func buildObservationMessage(record observable.ObservationRecord, opts attachmentOptions) (llm.Message, []string, error) {
	report := eventmedia.ValidateAttachments(record.Attachments, eventmedia.ValidationOptions{WorkDir: opts.WorkDir})
	text := renderObservationText(record, report)
	msg := eventMessageWithAttachments(llm.MessageKindObservation, text, report)
	errors := append([]string(nil), record.AttachmentErrors...)
	errors = append(errors, attachmentErrorMessages(report)...)
	return msg, uniqueStrings(errors), nil
}

func mcpNotificationMessage(n mcp.Notification, eventType string, opts attachmentOptions) (llm.Message, error) {
	refs, err := eventmedia.ExtractAttachmentRefs(n.Params["attachments"])
	report := eventmedia.ValidateAttachments(refs, eventmedia.ValidationOptions{WorkDir: opts.WorkDir})
	text := renderMCPNotificationText(n, eventType, report, err)
	return eventMessageWithAttachments(llm.MessageKindMCPEvent, text, report), nil
}

func eventMessageWithAttachments(kind string, text string, report eventmedia.ValidationReport) llm.Message {
	blocks := []llm.Block{{Type: llm.BlockText, Text: text}}
	for _, attachment := range report.Valid {
		if !eventmedia.IsImageMediaType(attachment.MediaType) {
			continue
		}
		blocks = append(blocks, llm.Block{
			Type: llm.BlockImage,
			Media: &llm.MediaRef{
				ArtifactPath:  attachment.ArtifactPath,
				MediaType:     attachment.MediaType,
				SHA256:        attachment.SHA256,
				OriginalBytes: attachment.OriginalBytes,
				Width:         attachment.Width,
				Height:        attachment.Height,
			},
		})
	}
	return llm.Message{Role: llm.RoleUser, Kind: kind, Blocks: blocks}
}

func renderObservationText(record observable.ObservationRecord, report eventmedia.ValidationReport) string {
	var sb strings.Builder
	sb.WriteString("Observable observation\n")
	fmt.Fprintf(&sb, "observation_id: %s\n", record.ID)
	fmt.Fprintf(&sb, "observable_id: %s\n", record.ObservableID)
	fmt.Fprintf(&sb, "kind: %s\n", record.Kind)
	fmt.Fprintf(&sb, "severity: %s\n", record.Severity)
	fmt.Fprintf(&sb, "window_start: %d\n", observationTimeMillis(record.WindowStart))
	fmt.Fprintf(&sb, "window_end: %d\n", observationTimeMillis(record.WindowEnd))
	if record.Truncated {
		sb.WriteString("truncated: true\n")
	}
	if record.ArtifactPath != "" {
		fmt.Fprintf(&sb, "artifact_path: %s\n", record.ArtifactPath)
	}
	sb.WriteString("content:\n")
	sb.WriteString(record.Content)
	if !strings.HasSuffix(record.Content, "\n") {
		sb.WriteByte('\n')
	}
	writeAttachmentSummary(&sb, report)
	if len(record.AttachmentErrors) > 0 {
		sb.WriteString("stored_attachment_errors:\n")
		for _, errText := range record.AttachmentErrors {
			fmt.Fprintf(&sb, "- %s\n", errText)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderMCPNotificationText(n mcp.Notification, eventType string, report eventmedia.ValidationReport, attachmentParseErr error) string {
	var sb strings.Builder
	sb.WriteString("MCP notification\n")
	fmt.Fprintf(&sb, "server: %s\n", n.ServerName)
	if n.Method != "" {
		fmt.Fprintf(&sb, "method: %s\n", n.Method)
	}
	if eventType != "" {
		fmt.Fprintf(&sb, "event_type: %s\n", eventType)
	}
	content := n.Content
	if value, ok := n.Params["content"].(string); ok && value != "" {
		content = value
	}
	if content != "" {
		sb.WriteString("content:\n")
		sb.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			sb.WriteByte('\n')
		}
	}
	if meta, ok := n.Params["meta"].(map[string]any); ok && len(meta) > 0 {
		sb.WriteString("meta:\n")
		writeSortedScalarMap(&sb, meta)
	}
	extra := notificationExtraParams(n.Params)
	if len(extra) > 0 {
		sb.WriteString("params:\n")
		writeSortedScalarMap(&sb, extra)
	}
	writeAttachmentSummary(&sb, report)
	if attachmentParseErr != nil {
		sb.WriteString("attachment_errors:\n")
		fmt.Fprintf(&sb, "- %s\n", attachmentParseErr.Error())
	}
	return strings.TrimRight(sb.String(), "\n")
}

func writeAttachmentSummary(sb *strings.Builder, report eventmedia.ValidationReport) {
	if len(report.Valid) > 0 {
		sb.WriteString("attachments:\n")
		for _, attachment := range report.Valid {
			kind := "file"
			if eventmedia.IsImageMediaType(attachment.MediaType) {
				kind = "image"
			}
			fmt.Fprintf(sb, "- %s source=%s artifact=%s (%s, %d bytes", kind, attachment.Ref.Path, attachment.ArtifactPath, attachment.MediaType, attachment.OriginalBytes)
			if attachment.SHA256 != "" {
				fmt.Fprintf(sb, ", sha256=%s", attachment.SHA256)
			}
			if attachment.Width > 0 && attachment.Height > 0 {
				fmt.Fprintf(sb, ", %dx%d", attachment.Width, attachment.Height)
			}
			sb.WriteString(")\n")
		}
	}
	if len(report.Errors) > 0 {
		sb.WriteString("attachment_errors:\n")
		for _, errInfo := range report.Errors {
			if errInfo.Path != "" {
				fmt.Fprintf(sb, "- %s: %s\n", errInfo.Path, errInfo.Error)
			} else {
				fmt.Fprintf(sb, "- %s\n", errInfo.Error)
			}
		}
	}
}

func attachmentErrorMessages(report eventmedia.ValidationReport) []string {
	if len(report.Errors) == 0 {
		return nil
	}
	out := make([]string, 0, len(report.Errors))
	for _, errInfo := range report.Errors {
		if errInfo.Path != "" {
			out = append(out, errInfo.Path+": "+errInfo.Error)
		} else {
			out = append(out, errInfo.Error)
		}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func notificationExtraParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	out := map[string]any{}
	for key, value := range params {
		switch key {
		case "content", "meta", "attachments":
			continue
		default:
			out[key] = value
		}
	}
	return out
}

func writeSortedScalarMap(sb *strings.Builder, values map[string]any) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(sb, "%s: %s\n", key, renderScalar(values[key]))
	}
}

func renderScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		body, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(body)
	}
}

func observationTimeMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().Truncate(time.Millisecond).UnixMilli()
}

func observationPendingInputID(record observable.ObservationRecord) string {
	return "observation-" + record.ID
}

func (a *App) markObservationAttachmentError(record observable.ObservationRecord, messages []string) {
	if a == nil || len(messages) == 0 {
		return
	}
	if a.obsv != nil {
		if err := a.obsv.MarkObservationAttachmentError(record.ID, messages); err == nil {
			return
		}
	}
	record.AttachmentState = observable.ObservationAttachmentStateError
	record.AttachmentErrors = append([]string(nil), messages...)
	record.Error = strings.Join(messages, "; ")
	if a.Bus != nil {
		a.Bus.Emit(events.Event{
			Type: observable.EventObservationErrored,
			Payload: observable.ObservationEventPayload{
				Observation: record,
				Error:       record.Error,
			},
		})
	}
}

func mcpNotificationPendingInputID(n mcp.Notification, eventType string) string {
	body, err := json.Marshal(struct {
		ServerName string         `json:"server_name"`
		Method     string         `json:"method,omitempty"`
		EventType  string         `json:"event_type,omitempty"`
		Content    string         `json:"content,omitempty"`
		Params     map[string]any `json:"params,omitempty"`
	}{
		ServerName: n.ServerName,
		Method:     n.Method,
		EventType:  eventType,
		Content:    n.Content,
		Params:     n.Params,
	})
	if err != nil {
		body = []byte(n.ServerName + ":" + eventType + ":" + n.Method + ":" + n.Content)
	}
	sum := sha256.Sum256(body)
	return "mcp-" + hex.EncodeToString(sum[:8])
}

func (a *App) TokenUsage() llm.Usage {
	info, ok := a.SessionInfo(time.Now().UTC())
	if !ok {
		return llm.Usage{}
	}
	return info.TokenUsage
}

func (a *App) MCPStatus() MCPStatus {
	if a == nil {
		return MCPStatus{}
	}
	status := a.mcp
	status.Servers = append([]MCPServerStatus(nil), status.Servers...)
	return status
}

func (a *App) Observables() *observable.Manager {
	if a == nil {
		return nil
	}
	return a.obsv
}

func buildMCPStatus(configured map[string]mcp.ServerSpec, toolCounts map[string]int, startupErrors map[string]string) MCPStatus {
	names := map[string]struct{}{}
	for name := range configured {
		names[name] = struct{}{}
	}
	for name := range toolCounts {
		names[name] = struct{}{}
	}
	for name := range startupErrors {
		names[name] = struct{}{}
	}

	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	configuredCount := len(configured)
	if configuredCount == 0 && len(names) > 0 {
		configuredCount = len(names)
	}
	status := MCPStatus{
		Configured: configuredCount,
		Servers:    make([]MCPServerStatus, 0, len(ordered)),
	}
	for _, name := range ordered {
		count, connected := toolCounts[name]
		errText := startupErrors[name]
		server := MCPServerStatus{
			Name:      name,
			Status:    "not_started",
			Connected: connected,
			ToolCount: count,
			Error:     errText,
		}
		if server.Connected {
			server.Status = "connected"
			status.Connected++
		} else if errText != "" {
			server.Status = "error"
			status.Errors++
		}
		status.Servers = append(status.Servers, server)
	}
	return status
}

func writeMCPStartupWarnings(w io.Writer, startupErrors map[string]string) {
	if w == nil || len(startupErrors) == 0 {
		return
	}
	names := make([]string, 0, len(startupErrors))
	for name := range startupErrors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "juex: warning: optional MCP server %q is unavailable: %s\n", name, startupErrors[name])
	}
}

func durationSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	max := time.Duration(tools.MaxTimeoutSeconds) * time.Second
	if d >= max {
		return tools.MaxTimeoutSeconds
	}
	seconds := d / time.Second
	if d%time.Second > 0 {
		seconds++
	}
	return int(seconds)
}

func FormatTokenUsage(usage llm.Usage) string {
	return fmt.Sprintf("tokens: %s total (input %s, output %s)",
		FormatCompactTokenCount(usage.TotalTokens()),
		FormatCompactTokenCount(usage.InputTokens),
		FormatCompactTokenCount(usage.OutputTokens))
}

// Close advances cleanup until it completes or an observable close must be
// deferred. A deferred result leaves later resources untouched so callback
// callers can return safely and an external owner can resume cleanup.
func (a *App) Close() (result error) {
	if a == nil {
		return nil
	}
	a.closeMu.Lock()
	if a.closeRunning {
		done := a.closeRunDone
		activeResult := a.closeRunResult
		a.closeMu.Unlock()
		return &CloseDeferredError{done: done, result: activeResult}
	}
	a.closeRunning = true
	a.closeRunDone = make(chan struct{})
	a.closeRunResult = &result
	done := a.closeRunDone
	a.closeMu.Unlock()
	defer func() {
		a.closeMu.Lock()
		a.closeRunning = false
		close(done)
		a.closeMu.Unlock()
	}()
	a.closeCancel.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
	})
	for {
		a.closeMu.Lock()
		if a.cleanupIndex >= len(a.cleanup) {
			result = a.closeErr
			a.closeMu.Unlock()
			return result
		}
		fn := a.cleanup[a.cleanupIndex]
		a.closeMu.Unlock()
		err := fn()
		var deferred interface{ Wait() error }
		if errors.As(err, &deferred) {
			return err
		}
		a.closeMu.Lock()
		a.cleanupIndex++
		if err != nil && a.closeErr == nil {
			a.closeErr = err
		}
		a.closeMu.Unlock()
	}
}

// CloseAndWait fully drains deferred observable work before releasing later
// resources. It is for process and transport owners, not callback code.
func (a *App) CloseAndWait() error {
	if a == nil {
		return nil
	}
	for {
		err := a.Close()
		var deferred interface{ Wait() error }
		if !errors.As(err, &deferred) {
			return err
		}
		_ = deferred.Wait()
	}
}
