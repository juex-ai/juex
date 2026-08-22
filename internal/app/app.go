// Package app wires process-level runtime dependencies: config -> provider ->
// enabled Modules -> sealed catalogs -> Session -> prompt -> engine.
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/eventmedia"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	"github.com/juex-ai/juex/internal/observability"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/provenance"
	"github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
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
	// ModelHealth may be shared by multiple Apps, as juex listen does.
	ModelCandidates []runtime.ModelCandidate
	ModelHealth     *llm.ModelHealth
	// SummaryProvider, when set, overrides compaction.summary_model provider
	// construction. It is primarily useful for tests and embedded callers.
	SummaryProvider      llm.Provider
	SummaryProvenance    provenance.SafeProvider
	SummaryContextWindow int
	Verbose              bool
	Debug                bool
	LogLevel             string
	Stderr               io.Writer
	WorkDir              string // if set, overrides Config.WorkDir
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
	// AgentRuntime reuses a process-lifetime resource and environment
	// resolution owned by a long-running caller such as the Web server.
	AgentRuntime *AgentRuntimeResolution

	// Internal child-runtime seams for managed Side Sessions.
	disableSideSessionTools bool
	disableObservables      bool
	sharedGoalState         *workmem.GoalStateStore
	sharedNotes             *workmem.NotesStore
	sharedObservables       *observable.Manager
	sideSessionFactory      sideSessionFactory
	sessionModuleFactories  []runtimemodule.SessionFactorySpec
	startupContext          context.Context
}

type SessionMode string

const (
	SessionModeAttachActive SessionMode = "attach_active"
	SessionModeNewPrimary   SessionMode = "new_primary"
	SessionModeNewSide      SessionMode = "new_side"
)

type App struct {
	Engine                 *runtime.Engine
	Status                 *runtime.StatusStore
	Bus                    *events.Bus
	Session                *session.Session
	cleanup                []func() error
	closeMu                sync.Mutex
	lifecycleMu            sync.RWMutex
	closeCancel            sync.Once
	cleanupIndex           int
	closeErr               error
	closeRunning           bool
	closeRunDone           chan struct{}
	closeRunResult         *error
	sessionReplaceMu       sync.Mutex
	sessionMu              sync.RWMutex
	sessionReleased        chan struct{}
	sessionRelease         sync.Once
	ctx                    context.Context
	cancel                 context.CancelFunc
	cfg                    config.Config
	stderr                 io.Writer
	skills                 []skills.Skill
	skillPrompt            skills.PromptBudgetReport
	skillFiltered          int
	skillFilteredItems     []skills.FilteredSkill
	mcp                    MCPStatus
	obsv                   *observable.Manager
	chunkedWrites          *tools.ChunkedWriteManager
	shellSessions          *tools.ShellSessionManager
	sideSessions           *sideSessionManager
	sideFactory            sideSessionFactory
	mcpManager             *mcp.Manager
	agentRuntime           AgentRuntimeResolution
	runtimeModules         *runtimemodule.Set
	runtimeModuleContext   runtimemodule.RuntimeContext
	sessionModuleFactories []runtimemodule.SessionFactorySpec
	hookRunner             hooks.PolicyRunner
	hookBaseRequest        hooks.Request

	turnAdmission   turnAdmission
	pendingRecovery sync.WaitGroup

	sessionLock       *session.Lock
	sessionResource   *session.Session
	eventSink         *events.DurableSink
	eventCatalog      events.SchemaCatalog
	eventUnsubscribe  func()
	statusUnsubscribe func()

	debug                    bool
	logLevel                 string
	runtimeEnvironment       environment.Snapshot
	recorder                 *observability.Recorder
	observabilityUnsubscribe func()
}

// RedactRuntimeJSON removes configured Agent environment values from a JSON
// diagnostic using this App's immutable process-lifetime resolution.
func (a *App) RedactRuntimeJSON(data []byte) ([]byte, bool, error) {
	if a == nil {
		return append([]byte(nil), data...), false, nil
	}
	return a.runtimeEnvironment.RedactConfiguredJSON(data)
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
	startupCtx := opts.startupContext
	if startupCtx == nil {
		startupCtx = context.Background()
	}
	if err := startupCtx.Err(); err != nil {
		return nil, err
	}
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
	if cfg.AgentStateDir == "" && cfg.AgentAddress.StateDir() != "" {
		cfg.AgentStateDir = cfg.AgentAddress.StateDir()
	}
	if err := ValidateModuleConfig(cfg); err != nil {
		return nil, err
	}
	runtimePaths := cfg.RuntimePaths()
	runtimeLimits := cfg.RuntimeLimits()
	var agentRuntime AgentRuntimeResolution
	if opts.AgentRuntime != nil {
		agentRuntime = *opts.AgentRuntime
	} else {
		var err error
		agentRuntime, err = ResolveAgentRuntime(cfg)
		if err != nil {
			return nil, err
		}
	}
	resourceGraph := agentRuntime.ResourceGraph()
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
			resolved.Selection.ArtifactDir = runtimePaths.ArtifactDir
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
				Provenance:      provenance.SafeProviderFromProfile(profile),
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
	summaryProvenance := opts.SummaryProvenance
	summaryContextWindow := opts.SummaryContextWindow
	if summaryProvider == nil && !providerInjected && strings.TrimSpace(runtimeLimits.Compaction.SummaryModel) != "" {
		resolved, err := cfg.ResolvedModelForRef(runtimeLimits.Compaction.SummaryModel)
		if err != nil {
			return nil, fmt.Errorf("app: compaction.summary_model: %w", err)
		}
		selection := resolved.Selection
		selection.ArtifactDir = runtimePaths.ArtifactDir
		profile, err := selection.ProviderProfile()
		if err != nil {
			return nil, fmt.Errorf("app: compaction.summary_model: %w", err)
		}
		p, err := llm.NewProvider(profile)
		if err != nil {
			return nil, fmt.Errorf("app: compaction.summary_model: %w", err)
		}
		summaryProvider = p
		summaryProvenance = provenance.SafeProviderFromProfile(profile)
		summaryContextWindow = resolved.ContextWindow
	}

	bus := events.NewBus()
	if opts.Verbose {
		vp := newVerbosePrinter(stderr)
		bus.Subscribe("*", vp.handle)
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	appContextTransferred := false
	var runtimeModules runtimeModuleComposition
	var err error
	defer func() {
		if !appContextTransferred {
			if runtimeModules.set != nil {
				_ = runtimeModules.set.CloseRuntime(context.Background())
			}
			appCancel()
		}
	}()
	toolTimeoutSeconds := durationSeconds(runtimeLimits.ToolTimeout)
	reg := tools.NewRegistryWithOptions(tools.RegistryOptions{
		DefaultTimeoutSeconds: toolTimeoutSeconds,
	})
	filePolicy := sandbox.NewFilePolicy(sandbox.FilePolicyOptions{Policy: cfg.SandboxPolicy(), WorkDir: runtimePaths.WorkDir, AgentStateDir: runtimePaths.StateDir})
	chunkedWrites := tools.NewChunkedWriteManager(runtimePaths.WorkDir, filePolicy)
	runtimeEnvironment := agentRuntime.Environment()
	sandboxRunner := sandbox.DefaultRunner{LookPath: cfg.LaunchEnvironmentSnapshot().LookPath}
	runtimeModules, err = prepareRuntimeModules(appCtx, cfg, resourceGraph, runtimePaths, runtimeEnvironment, sandboxRunner, chunkedWrites, toolTimeoutSeconds)
	if err != nil {
		return nil, err
	}

	var mcpConfigs []mcp.Config
	var mergedMCP mcp.Config
	if cfg.ModuleEnabled(string(mcp.ModuleID)) && !opts.DisableMCP && opts.MCPManager == nil {
		var err error
		mcpConfigs, mergedMCP, _, err = loadMCPConfigRefsForRuntime(resourceGraph.MCPConfigs(), runtimePaths.WorkDir, runtimeEnvironment)
		if err != nil {
			return nil, err
		}
	}

	attachment, sessLock, err := AttachAndLockWorkspaceSession(cfg, SessionAttachmentRequest{
		ResumeDir: opts.ResumeDir,
		Mode:      opts.SessionMode,
		Alias:     opts.Alias,
		Lazy:      opts.LazySession,
	})
	if err != nil {
		return nil, err
	}
	sess := attachment.Session
	if err := sess.ApplyAlias(opts.Alias); err != nil {
		_ = sessLock.Close()
		_ = sess.Close()
		return nil, err
	}
	var sessionModules *runtimemodule.Set
	var eventSink *events.DurableSink
	var eventUnsubscribe func()
	var statusUnsubscribe func()
	closeSessionResources := func() {
		if sessionModules != nil {
			_ = sessionModules.CloseSession(context.Background())
			sessionModules = nil
		}
		if statusUnsubscribe != nil {
			statusUnsubscribe()
			statusUnsubscribe = nil
		}
		if eventUnsubscribe != nil {
			eventUnsubscribe()
			eventUnsubscribe = nil
		}
		if eventSink != nil {
			_ = eventSink.Close()
			eventSink = nil
		}
		_ = sessLock.Close()
		_ = sess.Close()
	}
	eventCatalog := eventcatalog.Default()
	eventSink = events.NewDurableSink(sess)
	eventSink.SetCatalog(eventCatalog)
	bus.SetCommitter(eventSink)
	eventUnsubscribe = func() { bus.SetCommitter(nil) }
	status, statusReplayErr := runtime.NewStatusStoreFromReplay(
		runtimeStatusSeed(sess, runtime.DefaultMaxPendingInput),
		func(visit func(events.Event)) error {
			return session.ReplayEventsWithCatalog(sess.Dir, eventCatalog, visit)
		},
	)
	if statusReplayErr != nil {
		fmt.Fprintf(stderr, "juex: warning: restore runtime status: %v; continuing with recovered events\n", statusReplayErr)
	}
	var eng *runtime.Engine
	pb := &prompt.Builder{
		ModulePromptContext: func() ([]runtimemodule.ContextSection, error) {
			request := runtimemodule.ContextRequest{
				Purpose: runtimemodule.ContextPurposeProviderIteration,
				Runtime: runtimeModules.runtimeContext,
			}
			var activeSessionModules *runtimemodule.Set
			if eng != nil {
				snapshot := eng.SessionRuntimeSnapshot()
				activeSessionModules = snapshot.Modules
				sessionContext := sessionModuleContext(snapshot.Session)
				request.Session = &sessionContext
			}
			return runtimemodule.CollectContext(appCtx, request, runtimeModules.set, activeSessionModules)
		},
	}
	var hookRunner hooks.PolicyRunner
	if cfg.ModuleEnabled(string(hooks.ModuleID)) {
		hookRunner, err = hooks.NewRunnerWithOptions(resourceGraph.HooksConfig(), hooks.RunnerOptions{
			Environment: runtimeEnvironment,
		})
		if err != nil {
			closeSessionResources()
			return nil, err
		}
	}
	hookBaseRequest := hooks.Request{
		CWD:            runtimePaths.WorkDir,
		WorkspaceRoots: []string{runtimePaths.WorkDir},
		PermissionMode: "unrestricted",
		SandboxMode:    "none",
	}
	pendingInputTTL := runtimeLimits.PendingInputTTL
	if pendingInputTTL <= 0 {
		pendingInputTTL = runtime.DefaultPendingInputTTL
	}
	externalEventTTL := runtimeLimits.ExternalEventTTL
	if externalEventTTL <= 0 {
		externalEventTTL = runtime.DefaultExternalEventTTL
	}

	eng = &runtime.Engine{
		Provider:                provider,
		SummaryProvider:         summaryProvider,
		SummaryProvenance:       summaryProvenance,
		SummaryContextWindow:    summaryContextWindow,
		ModelCandidates:         modelCandidates,
		ModelHealth:             modelHealth,
		Tools:                   reg,
		RuntimeContext:          runtimeModules.runtimeContext,
		Bus:                     bus,
		Session:                 sess,
		Prompt:                  pb,
		WorkDir:                 runtimePaths.WorkDir,
		ArtifactDir:             runtimePaths.ArtifactDir,
		PendingInputQueue:       runtime.NewPendingInputQueue(sess.Dir, runtime.PendingInputQueueOptions{}),
		PendingInputTTL:         pendingInputTTL,
		ExternalEventTTL:        externalEventTTL,
		ShowBuiltinPolicyTraces: runtimeLimits.ShowBuiltinPolicyTraces,
		NotifyModelChanges:      runtimeLimits.NotifyModelChanges,
		ContextWindow:           runtimeLimits.ContextWindow,
		MaxOutputTokens:         runtimeLimits.MaxOutputTokens,
		Compaction:              runtimeLimits.Compaction,
		ToolOutput:              runtimeLimits.ToolOutput,
	}
	a := &App{
		Engine:                 eng,
		Status:                 status,
		Bus:                    bus,
		Session:                sess,
		ctx:                    appCtx,
		cancel:                 appCancel,
		cfg:                    cfg,
		stderr:                 stderr,
		chunkedWrites:          chunkedWrites,
		sessionLock:            sessLock,
		sessionResource:        sess,
		eventSink:              eventSink,
		eventCatalog:           eventCatalog,
		eventUnsubscribe:       eventUnsubscribe,
		statusUnsubscribe:      statusUnsubscribe,
		debug:                  opts.Debug,
		logLevel:               opts.LogLevel,
		runtimeEnvironment:     runtimeEnvironment,
		sessionReleased:        make(chan struct{}),
		sideFactory:            opts.sideSessionFactory,
		mcpManager:             opts.MCPManager,
		agentRuntime:           agentRuntime,
		runtimeModuleContext:   runtimeModules.runtimeContext,
		sessionModuleFactories: append([]runtimemodule.SessionFactorySpec(nil), opts.sessionModuleFactories...),
		hookRunner:             hookRunner,
		hookBaseRequest:        hookBaseRequest,
	}
	statusUnsubscribe = eventSink.AddProjection(turnAdmissionStatusProjection{
		status:       status,
		completeTurn: a.CompleteAdmittedTurn,
	})
	a.statusUnsubscribe = statusUnsubscribe
	if err := a.attachObservability(sess); err != nil {
		closeSessionResources()
		return nil, err
	}
	a.mcp = buildMCPStatus(mergedMCP.MCPServers, nil, nil)
	a.cleanup = append(a.cleanup, func() error {
		if a.runtimeModules == nil {
			return nil
		}
		return a.runtimeModules.QuiesceRuntime(context.Background())
	}, a.waitPendingInputRecovery, func() error {
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
			return a.eventSink.Close()
		}
		return nil
	}, a.closeActiveSessionResources, func() error {
		if a.runtimeModules == nil {
			return nil
		}
		return a.runtimeModules.CloseRuntime(context.Background())
	})

	var notificationGate *mcpNotificationGate
	connectOpts := mcp.ConnectOptions{
		Stderr:        stderr,
		ForwardStderr: opts.Verbose,
		Environment:   runtimeEnvironment,
	}
	if sess.Kind == session.KindPrimary {
		connectOpts.EnableClaudeChannel = true
		notificationGate = newMCPNotificationGate(func(n mcp.Notification) {
			_ = a.HandleMCPNotification(a.ctx, n)
		})
		connectOpts.OnNotification = notificationGate.Enqueue
	}
	var mcpRuntimeModule *mcp.Module
	var observableRuntimeModule *observable.Module
	extraRuntimeSpecs := []runtimemodule.RuntimeFactorySpec{
		{
			ID:      mcp.ModuleID,
			Enabled: cfg.ModuleEnabled(string(mcp.ModuleID)) && (opts.MCPManager != nil || !opts.DisableMCP),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				if opts.MCPManager != nil {
					mcpRuntimeModule = mcp.NewModule(opts.MCPManager)
				} else {
					mcpRuntimeModule = mcp.NewRuntimeModule(mcpConfigs, connectOpts)
				}
				return mcpRuntimeModule, nil
			},
		},
		{
			ID:      observable.ModuleID,
			Enabled: cfg.ModuleEnabled(string(observable.ModuleID)) && (opts.sharedObservables != nil || !opts.disableObservables),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				if opts.sharedObservables != nil {
					observableRuntimeModule = observable.NewModule(opts.sharedObservables)
				} else {
					observableRuntimeModule = observable.NewRuntimeModule(observable.ManagerOptions{
						ConfigPath:            cfg.ObservablesConfigPath(),
						ReadOnlyConfigSources: observableReadOnlyConfigSources(resourceGraph.ObservableConfigs()),
						StateDir:              cfg.ObservablesStateDir(),
						WorkDir:               runtimePaths.WorkDir,
						AgentStateDir:         runtimePaths.StateDir,
						ArtifactDir:           runtimePaths.ArtifactDir,
						Environment:           runtimeEnvironment,
						Shell:                 cfg.Shell,
						Sandbox:               cfg.SandboxPolicy(),
						SandboxRunner:         sandboxRunner,
						Bus:                   bus,
						Deliver:               a.DeliverObservation,
					}, sess.Kind == session.KindPrimary)
				}
				return observableRuntimeModule, nil
			},
		},
		{
			ID:      sideSessionModuleID,
			Enabled: cfg.ModuleEnabled(string(sideSessionModuleID)) && sess.Kind == session.KindPrimary && sess.Active && !opts.disableSideSessionTools,
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				a.sideSessions = newSideSessionManager(a)
				return &sideSessionModule{manager: a.sideSessions}, nil
			},
		},
	}
	if err := runtimeModules.sealAndStart(startupCtx, extraRuntimeSpecs...); err != nil {
		_ = a.Close()
		return nil, err
	}
	a.runtimeModules = runtimeModules.set
	eng.RuntimeModules = runtimeModules.set
	if runtimeModules.skills != nil {
		a.skills = runtimeModules.skills.All()
		a.skillPrompt = runtimeModules.skills.PromptReport()
		a.skillFilteredItems = runtimeModules.skills.Filtered()
		a.skillFiltered = len(a.skillFilteredItems)
	}
	if runtimeModules.builtinTools != nil {
		a.shellSessions = runtimeModules.builtinTools.ShellSessions()
	}
	if observableRuntimeModule != nil {
		a.obsv = observableRuntimeModule.Manager()
	}
	if mcpRuntimeModule != nil {
		a.mcpManager = mcpRuntimeModule.Manager()
		startupErrors := a.mcpManager.StartupErrors()
		if !opts.SuppressMCPWarnings {
			writeMCPStartupWarnings(stderr, startupErrors, runtimeEnvironment)
		}
		a.mcp = buildMCPStatus(mergedMCP.MCPServers, a.mcpManager.ToolCounts(), startupErrors)
	}
	sessionModules, err = buildSessionModules(
		startupCtx,
		cfg,
		opts.sessionModuleFactories,
		runtimeModules.runtimeContext,
		sess,
		eng,
		runtimePaths.WorkDir,
		promptcontext.ShellProfileFromConfig(cfg.Shell),
		a.shellSessions,
		sessionModuleOptions{
			hookRunner:               hookRunner,
			hookBaseRequest:          hookBaseRequest,
			goalState:                opts.sharedGoalState,
			notes:                    opts.sharedNotes,
			goalContinuation:         opts.sharedGoalState == nil,
			goalContinuationDeferrer: a.sideSessions,
		},
	)
	if err != nil {
		_ = a.Close()
		return nil, err
	}
	if err := validateSessionModuleContext(startupCtx, runtimeModules.set, sessionModules, runtimeModules.runtimeContext, sess); err != nil {
		_ = sessionModules.CloseSession(context.Background())
		_ = a.Close()
		return nil, err
	}
	reg, err = runtimemodule.BuildToolRegistry(tools.RegistryOptions{DefaultTimeoutSeconds: toolTimeoutSeconds}, runtimeModules.set, sessionModules)
	if err != nil {
		_ = sessionModules.CloseSession(context.Background())
		_ = a.Close()
		return nil, err
	}
	if err := eng.ReplaceSessionRuntimeBundle(sess, runtime.SessionRuntimeReplacement{Modules: sessionModules, Tools: reg}); err != nil {
		_ = sessionModules.CloseSession(context.Background())
		_ = a.Close()
		return nil, err
	}
	if err := eng.RecoverTranscript("load"); err != nil {
		_ = a.Close()
		return nil, err
	}
	replayablePendingInput, err := eng.RecoverPendingInputRecords()
	if err != nil {
		_ = a.Close()
		return nil, err
	}
	status.RecoverAfterRestart()
	chunkedWrites.RestoreActiveFromHistory(sess.History)
	if err := eng.RunSessionStartPolicies(startupCtx); err != nil {
		_ = a.Close()
		return nil, err
	}
	if err := startupCtx.Err(); err != nil {
		_ = a.Close()
		return nil, err
	}
	appContextTransferred = true
	if notificationGate != nil {
		notificationGate.Activate()
	}
	if len(replayablePendingInput) > 0 {
		a.startPendingInputRecovery(replayablePendingInput[0])
	}
	return a, nil
}

func goalStateStore(sess *session.Session) *workmem.GoalStateStore {
	if sess == nil || sess.Dir == "" {
		return nil
	}
	return workmem.NewGoalStateStore(sess.Dir, workmem.GoalStateOptions{})
}

func notesStore(sess *session.Session) *workmem.NotesStore {
	if sess == nil || sess.Dir == "" {
		return nil
	}
	return workmem.NewNotesStore(sess.Dir)
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
	return a.SwitchToNewPrimarySessionContext(context.Background())
}

func (a *App) SwitchToNewPrimarySessionContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()
	a.sessionReplaceMu.Lock()
	defer a.sessionReplaceMu.Unlock()
	var oldInfo session.Info
	err := a.ReadSession(func(sess *session.Session) error {
		oldInfo = sess.Info()
		return nil
	})
	if err != nil {
		return fmt.Errorf("app: nil session")
	}
	if oldInfo.Kind == session.KindSide {
		return fmt.Errorf("side sessions cannot switch workspace active session")
	}
	replace := func() error {
		attachment, sessLock, err := AttachAndLockWorkspaceSession(a.cfg, SessionAttachmentRequest{Mode: SessionModeNewPrimary})
		if err != nil {
			return err
		}
		sess := attachment.Session
		if err := a.replaceSession(ctx, sess, sessLock); err != nil {
			_ = sessLock.Close()
			_ = sess.Close()
			cleanupErr := DeleteSession(a.cfg, sess.ID, SessionDeleteOptions{AllowMissingSession: true})
			restoreErr := session.SetActive(a.cfg.HistoryPath(), oldInfo)
			return errors.Join(err, cleanupErr, restoreErr)
		}
		return nil
	}
	if a.sideSessions != nil {
		return a.sideSessions.replacePrimary(ctx, replace)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return replace()
}

func (a *App) replaceSession(ctx context.Context, sess *session.Session, sessLock *session.Lock) error {
	if ctx == nil {
		ctx = context.Background()
	}
	nextModules, err := buildSessionModules(
		ctx,
		a.cfg,
		a.sessionModuleFactories,
		a.runtimeModuleContext,
		sess,
		a.Engine,
		a.cfg.WorkDir,
		promptcontext.ShellProfileFromConfig(a.cfg.Shell),
		a.shellSessions,
		sessionModuleOptions{
			hookRunner:               a.hookRunner,
			hookBaseRequest:          a.hookBaseRequest,
			goalContinuation:         true,
			goalContinuationDeferrer: a.sideSessions,
		},
	)
	if err != nil {
		return err
	}
	if err := validateSessionModuleContext(ctx, a.runtimeModules, nextModules, a.runtimeModuleContext, sess); err != nil {
		return errors.Join(err, nextModules.CloseSession(context.Background()))
	}
	var nextTools *tools.Registry
	var oldRuntime runtime.SessionRuntimeSnapshot
	var oldRuntimeCheckpoint runtime.SessionRuntimeCheckpoint
	if a.Engine != nil {
		oldRuntimeCheckpoint = a.Engine.CaptureSessionRuntimeCheckpoint()
		oldRuntime = oldRuntimeCheckpoint.Snapshot()
		nextTools, err = runtimemodule.BuildToolRegistry(
			tools.RegistryOptions{DefaultTimeoutSeconds: durationSeconds(a.cfg.RuntimeLimits().ToolTimeout)},
			a.runtimeModules,
			nextModules,
		)
		if err != nil {
			return errors.Join(err, nextModules.CloseSession(context.Background()))
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, nextModules.CloseSession(context.Background()))
	}

	a.sessionMu.Lock()
	if a.Engine != nil {
		if err := a.Engine.ReplaceSessionRuntimeBundle(sess, runtime.SessionRuntimeReplacement{Modules: nextModules, Tools: nextTools}); err != nil {
			a.sessionMu.Unlock()
			return errors.Join(err, nextModules.CloseSession(context.Background()))
		}
	}
	if a.eventSink != nil {
		a.eventSink.SetJournal(sess)
	}
	oldLock := a.sessionLock
	oldSession := a.sessionResource
	observabilityErr := a.detachObservability()
	observabilityErr = errors.Join(observabilityErr, a.attachObservability(sess))
	rollbackReplacement := func(cause error) error {
		var rollbackErr error
		runtimeRestored := a.Engine == nil
		if err := a.detachObservability(); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore observability: detach replacement: %w", err))
		}
		if a.eventSink != nil {
			a.eventSink.SetJournal(oldRuntime.Session)
		}
		if a.Engine != nil {
			err := a.Engine.RestoreSessionRuntimeCheckpoint(oldRuntimeCheckpoint)
			if err != nil {
				rollbackErr = errors.Join(rollbackErr, err)
			} else {
				runtimeRestored = true
			}
		}
		if err := a.attachObservability(oldSession); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore observability: attach previous session: %w", err))
		}
		a.sessionMu.Unlock()
		if runtimeRestored {
			rollbackErr = errors.Join(rollbackErr, nextModules.CloseSession(context.Background()))
		}
		if rollbackErr != nil {
			return errors.Join(cause, fmt.Errorf("app: roll back rejected session replacement: %w", rollbackErr))
		}
		return cause
	}
	if a.Engine != nil {
		if err := a.Engine.RunSessionStartPolicies(ctx); err != nil {
			return rollbackReplacement(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return rollbackReplacement(err)
	}

	a.Session = sess
	a.sessionLock = sessLock
	a.sessionResource = sess
	if a.chunkedWrites != nil {
		a.chunkedWrites.RestoreActiveFromHistory(sess.History)
	}
	if a.Status != nil {
		err := a.Status.ResetFromReplayWithRestartRecovery(
			runtimeStatusSeed(sess, runtime.DefaultMaxPendingInput),
			func(visit func(events.Event)) error {
				return session.ReplayEventsWithCatalog(sess.Dir, a.eventCatalog, visit)
			},
		)
		if err != nil {
			fmt.Fprintf(a.stderr, "juex: warning: restore runtime status: %v; continuing with recovered events\n", err)
		}
	}
	if observabilityErr != nil {
		// Recorder failures do not reject an otherwise committed replacement.
		fmt.Fprintf(a.stderr, "juex: warning: session observability after committed replacement: %v\n", observabilityErr)
	}
	a.sessionMu.Unlock()

	var cleanupErr error
	if oldRuntime.Modules != nil {
		cleanupErr = errors.Join(cleanupErr, oldRuntime.Modules.CloseSession(context.Background()))
	}
	if oldLock != nil {
		cleanupErr = errors.Join(cleanupErr, oldLock.Close())
	}
	if oldSession != nil {
		cleanupErr = errors.Join(cleanupErr, oldSession.Close())
	}
	if cleanupErr != nil {
		fmt.Fprintf(a.stderr, "juex: warning: committed session replacement cleanup: %v\n", cleanupErr)
	}
	return nil
}

func (a *App) closeActiveSessionResources() error {
	if a == nil {
		return nil
	}
	a.sessionMu.Lock()
	sessLock := a.sessionLock
	sess := a.sessionResource
	a.sessionLock = nil
	a.sessionResource = nil
	a.Session = nil
	a.sessionMu.Unlock()

	var moduleErr, lockErr, sessionErr error
	if a.Engine != nil {
		if modules := a.Engine.SessionRuntimeSnapshot().Modules; modules != nil {
			moduleErr = modules.CloseSession(context.Background())
		}
	}
	if sessLock != nil {
		lockErr = sessLock.Close()
	}
	if sess != nil {
		sessionErr = sess.Close()
	}
	a.sessionRelease.Do(func() {
		if a.sessionReleased != nil {
			close(a.sessionReleased)
		}
	})
	return errors.Join(moduleErr, lockErr, sessionErr)
}

// WaitSessionReleased waits until final App cleanup has closed the active
// Session and its workspace lock, and until Side Session result deliveries can
// no longer write its directory. Child runtimes may still be draining.
func (a *App) WaitSessionReleased(ctx context.Context) error {
	if a == nil || a.sessionReleased == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-a.sessionReleased:
	case <-ctx.Done():
		return ctx.Err()
	}
	if a.sideSessions == nil {
		return nil
	}
	return a.sideSessions.WaitDeliveryWriters(ctx)
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

func (a *App) AddEventProjection(projection events.Delivery) func() {
	if a == nil || a.eventSink == nil {
		return func() {}
	}
	return a.eventSink.AddProjection(projection)
}

func (a *App) ReadCommittedEvents(read func() error) error {
	if a == nil || a.eventSink == nil {
		return events.ErrDurableSinkClosed
	}
	return a.eventSink.ReadCommitted(read)
}

func (a *App) attachObservability(sess *session.Session) error {
	if a == nil || a.Bus == nil || sess == nil {
		return nil
	}
	rec, err := observability.NewRecorder(observability.Options{
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
	if err := usermedia.ValidateSessionMediaRefs(a.cfg.ArtifactDir(), a.Session.ID, attachments, usermedia.Limits{}); err != nil {
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
	if a.Session == nil {
		return "", ErrSessionUnavailable
	}
	return a.Engine.Turn(ctx, input)
}

func (a *App) runEngineTurnMessage(ctx context.Context, message llm.Message) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: turn requires an initialized engine")
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.Session == nil {
		return "", ErrSessionUnavailable
	}
	return a.Engine.TurnMessage(ctx, message)
}

func (a *App) CompactWithInstructions(ctx context.Context, reason string, auto bool, instructions string) (runtime.CompactionResult, error) {
	if a == nil || a.Engine == nil {
		return runtime.CompactionResult{}, fmt.Errorf("app: nil engine")
	}
	admitted := events.Normalize(events.Event{Type: runtime.TurnAdmittedType, Payload: runtime.TurnAdmittedPayload{}})
	turnID := "compact-" + admitted.ID
	admitted.TurnID = turnID
	if err := a.Bus.Emit(admitted); err != nil {
		return runtime.CompactionResult{}, fmt.Errorf("commit compaction admission: %w", err)
	}
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
	if a.Session == nil {
		return runtime.CompactionResult{}, a.emitCompactionError(turnID, ErrSessionUnavailable)
	}
	sections, err := a.Engine.PromptSectionsWithError()
	if err != nil {
		return runtime.CompactionResult{}, a.emitCompactionError(turnID, fmt.Errorf("app: build compaction prompt: %w", err))
	}
	systemPrompt := prompt.JoinSections(sections)
	result, err := a.Engine.CompactWithInstructions(ctx, turnID, systemPrompt, reason, auto, instructions)
	if err != nil {
		return result, a.emitCompactionError(turnID, err)
	}
	if err := a.Bus.Emit(events.Event{Type: "turn.completed", TurnID: turnID, Payload: runtime.TurnCompletedPayload{
		TokenUsage: a.Session.TokenUsageSnapshot(),
	}}); err != nil {
		return result, fmt.Errorf("commit compaction completion: %w", err)
	}
	return result, nil
}

func (a *App) emitCompactionError(turnID string, err error) error {
	if emitErr := a.Bus.Emit(events.Event{Type: "turn.errored", TurnID: turnID, Payload: runtime.NewTurnErroredPayload(err)}); emitErr != nil {
		return errors.Join(err, fmt.Errorf("commit compaction error: %w", emitErr))
	}
	return err
}

func (a *App) HandleMCPNotification(ctx context.Context, n mcp.Notification) error {
	if a == nil || a.Engine == nil {
		return nil
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.Session == nil {
		return ErrSessionUnavailable
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	eventType := n.EventType
	if eventType == "" {
		eventType = "notification"
	}
	msg, err := mcpNotificationMessage(n, eventType, a.externalAttachmentOptions())
	if err != nil {
		return err
	}
	_, err = a.deliverExternalInputLocked(ctx, msg, runtime.PendingInputOptions{
		ID:  mcpNotificationPendingInputID(n, eventType),
		TTL: a.Engine.ExternalEventTTL,
	})
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
	msg, attachmentErrors, err := buildObservationMessage(record, a.externalAttachmentOptions())
	if err != nil {
		return observable.DeliveryOutcome{}, err
	}
	if len(attachmentErrors) > 0 {
		a.markObservationAttachmentError(record, attachmentErrors)
	}
	pendingID := observationPendingInputID(record)
	delivery, err := a.deliverExternalInputLocked(ctx, msg, runtime.PendingInputOptions{
		ID:  pendingID,
		TTL: a.Engine.ExternalEventTTL,
	})
	if delivery.Queued {
		return observable.DeliveryOutcome{
			State:          observable.ObservationStateQueued,
			PendingInputID: pendingID,
			TargetSession:  targetSession,
		}, err
	}
	if delivery.Delivered {
		return observable.DeliveryOutcome{
			State:          observable.ObservationStateDelivered,
			PendingInputID: pendingID,
			TargetSession:  targetSession,
		}, err
	}
	return observable.DeliveryOutcome{}, err
}

type attachmentOptions struct {
	WorkDir       string
	AgentStateDir string
	ArtifactDir   string
	PathGuard     sandbox.PathGuard
}

func (a *App) externalAttachmentOptions() attachmentOptions {
	paths := a.cfg.RuntimePaths()
	return attachmentOptions{
		WorkDir:       paths.WorkDir,
		AgentStateDir: paths.StateDir,
		ArtifactDir:   paths.ArtifactDir,
		PathGuard:     sandbox.NewFilePolicy(sandbox.FilePolicyOptions{Policy: a.cfg.SandboxPolicy(), WorkDir: paths.WorkDir, AgentStateDir: paths.StateDir}),
	}
}

func observationMessage(record observable.ObservationRecord, opts attachmentOptions) (llm.Message, error) {
	msg, _, err := buildObservationMessage(record, opts)
	return msg, err
}

func buildObservationMessage(record observable.ObservationRecord, opts attachmentOptions) (llm.Message, []string, error) {
	report := eventmedia.ValidateStoredAttachments(record.Attachments, eventmedia.ValidationOptions{ArtifactDir: opts.ArtifactDir})
	text := renderObservationText(record, report)
	msg := eventMessageWithAttachments(llm.MessageKindObservation, text, report)
	errors := append([]string(nil), record.AttachmentErrors...)
	errors = append(errors, attachmentErrorMessages(report)...)
	return msg, uniqueStrings(errors), nil
}

func mcpNotificationMessage(n mcp.Notification, eventType string, opts attachmentOptions) (llm.Message, error) {
	refs, err := eventmedia.ExtractAttachmentRefs(n.Params["attachments"])
	report := eventmedia.ValidateAttachments(refs, eventmedia.ValidationOptions{WorkDir: opts.WorkDir, AgentStateDir: opts.AgentStateDir, ArtifactDir: opts.ArtifactDir, PathGuard: opts.PathGuard})
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
		_ = a.Bus.Emit(events.Event{
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
	info, ok := a.SessionInfo()
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

func writeMCPStartupWarnings(w io.Writer, startupErrors map[string]string, runtimeEnvironment environment.Snapshot) {
	if w == nil || len(startupErrors) == 0 {
		return
	}
	names := make([]string, 0, len(startupErrors))
	for name := range startupErrors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		message := startupErrors[name]
		if redacted, changed := runtimeEnvironment.RedactConfiguredValues([]byte(message)); changed {
			message = string(redacted)
		}
		fmt.Fprintf(w, "juex: warning: optional MCP server %q is unavailable: %s\n", name, message)
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

// BeginClose cancels App-owned work without waiting for active turns or
// deferred cleanup to drain.
func (a *App) BeginClose() error {
	if a == nil {
		return nil
	}
	a.closeCancel.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		if a.runtimeModules != nil {
			// BeginClose is intentionally non-blocking. Close/CloseAndWait
			// serializes with this generic Module quiesce pass and reports its
			// cached result before releasing later resources.
			go func() { _ = a.runtimeModules.QuiesceRuntime(context.Background()) }()
		}
	})
	return nil
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
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	_ = a.BeginClose()
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
