package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/version"
)

// Options configures a Server. Provider is optional; if unset, each session
// resolves a provider profile from config and constructs a provider in app.
type Options struct {
	Cfg          config.Config
	Addr         string
	Provider     llm.Provider // optional; injected for tests
	AllowAnyBind bool         // bypass the loopback bind check; CLI sets this for --unsafe-bind-any
	Verbose      bool
	Debug        bool
	LogLevel     string
	Stderr       io.Writer
	OnReady      func(ReadyInfo)
}

type ReadyInfo struct {
	AgentEndpoint  string
	TCPAddress     string
	FallbackReason string
}

// Server is a long-running HTTP server for one WorkDir.
type Server struct {
	opts        Options
	modelHealth *llm.ModelHealth
	sessions    sync.Map // session id (string) → *activeSession
	nextTurn    atomic.Uint64
	startedAt   time.Time
	statusHub   *agentStatusHub

	createMu sync.Mutex // serialises POST /api/sessions
	closeMu  sync.Mutex
	closed   bool

	runtimeMu     sync.Mutex
	runtimeMCPErr map[string]string
	runtimeSkills *app.RuntimeStatusSkillCache

	mcpMu       sync.Mutex
	mcpStarted  bool
	mcpStarting chan struct{}
	mcpStartErr error
	mcpManager  *mcp.Manager

	endpointMu       sync.RWMutex
	endpointRuntime  endpoint.Runtime
	endpointShutdown chan struct{}
}

// activeSession wraps an app.App with the bookkeeping the web server
// needs for SSE fan-out and turn cancellation.
type activeSession struct {
	app       *app.App
	bcast     *broadcaster
	StartedAt time.Time

	turns             *webTurnTransport
	statusUnsubscribe func()
	workCtx           context.Context
	workCancel        context.CancelFunc
	workWG            sync.WaitGroup
	closeOnce         sync.Once
}

func NewServer(opts Options) *Server {
	return &Server{
		opts:          opts,
		modelHealth:   llm.NewModelHealth(llm.ModelHealthOptions{}),
		startedAt:     time.Now().UTC(),
		statusHub:     newAgentStatusHub(),
		runtimeMCPErr: map[string]string{},
		runtimeSkills: app.NewRuntimeStatusSkillCache(),
	}
}

// Handler returns the TCP agent API with a pointer for non-API browser routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)
	mux.HandleFunc("/", s.handleAgentAPIPointer)
	return mux
}

// APIHandler returns the canonical local agent API without a browser fallback.
func (s *Server) APIHandler() http.Handler {
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)
	return mux
}

// NewReadOnlyAPIHandler serves persisted session data without starting an agent
// runtime. It intentionally exposes only the durable GET endpoints needed to
// inspect stopped agents through the fleet UI.
func NewReadOnlyAPIHandler(cfg config.Config) http.Handler {
	server := NewServer(Options{Cfg: cfg})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		server.listSessions(w, r)
	})
	mux.HandleFunc("/api/sessions/", server.dispatchReadOnlySession)
	mux.HandleFunc("/api/media", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or HEAD required")
			return
		}
		server.handleMedia(w, r)
	})
	return mux
}

func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/api/identity", s.handleEndpointIdentity)
	mux.HandleFunc("/api/control/shutdown", s.handleEndpointShutdown)
	mux.HandleFunc("/api/sessions", s.handleListSessions)
	mux.HandleFunc("/api/sessions/", s.dispatchSession)
	mux.HandleFunc("/api/files/tree", s.handleFilesTree)
	mux.HandleFunc("/api/files/content", s.handleFilesContent)
	mux.HandleFunc("/api/files/raw", s.handleFilesRaw)
	mux.HandleFunc("/api/media", s.handleMedia)
	mux.HandleFunc("/api/activity", s.handleAgentActivity)
	mux.HandleFunc("/api/status", s.handleAgentStatus)
	mux.HandleFunc("/api/status/events", s.handleAgentStatusEvents)
	mux.HandleFunc("/api/runtime", s.handleRuntimeStatus)
	mux.HandleFunc("/api/observables", s.handleObservables)
	mux.HandleFunc("/api/observables/", s.dispatchObservable)
}

func (s *Server) dispatchReadOnlySession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	id, rest := sessionPathID(r.URL.Path)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing session id")
		return
	}
	switch rest {
	case "":
		s.handleSessionShow(w, r, id)
	case "context":
		s.handleSessionContext(w, r, id)
	case "scratchpad":
		s.handleSessionScratchpad(w, r, id)
	default:
		writeErr(w, http.StatusNotFound, "not_found", "read-only API route not found")
	}
}

func (s *Server) handleAgentAPIPointer(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	fmt.Fprintln(w, "Juex agent JSON/SSE API (no web UI).")
	fmt.Fprintln(w, "API routes are available under /api/.")
	fleetAddr := strings.TrimSpace(s.opts.Cfg.Fleet.Addr)
	if fleetAddr == "" {
		fleetAddr = config.DefaultFleetAddr
	}
	fmt.Fprintf(w, "For the browser UI, run `juex fleet serve` and open http://%s/.\n", fleetAddr)
}

// dispatchSession routes /api/sessions/<id>[/...] to the matching handler.
func (s *Server) dispatchSession(w http.ResponseWriter, r *http.Request) {
	id, rest := sessionPathID(r.URL.Path)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing session id")
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		s.handleSessionShow(w, r, id)
	case rest == "" && r.Method == http.MethodDelete:
		s.handleDeleteSession(w, r, id)
	case rest == "activate" && r.Method == http.MethodPost:
		s.handleActivateSession(w, r, id)
	case strings.HasPrefix(rest, "turns/") && r.Method == http.MethodGet:
		s.handleTurnStatus(w, r, id, strings.TrimPrefix(rest, "turns/"))
	case rest == "turns" && r.Method == http.MethodPost:
		s.handleStartTurn(w, r, id)
	case rest == "attachments" && r.Method == http.MethodPost:
		s.handleSessionAttachmentUpload(w, r, id)
	case rest == "interrupt" && r.Method == http.MethodPost:
		s.handleInterrupt(w, r, id)
	case rest == "events" && r.Method == http.MethodGet:
		s.handleEventsSSE(w, r, id)
	case rest == "status" && r.Method == http.MethodGet:
		s.handleSessionStatus(w, r, id)
	case rest == "status/events" && r.Method == http.MethodGet:
		s.handleSessionStatusEvents(w, r, id)
	case rest == "compact" && r.Method == http.MethodPost:
		s.handleCompactSession(w, r, id)
	case rest == "context" && r.Method == http.MethodGet:
		s.handleSessionContext(w, r, id)
	case rest == "scratchpad" && r.Method == http.MethodGet:
		s.handleSessionScratchpad(w, r, id)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method or sub-path")
	}
}

// Run starts the canonical agent API endpoint and an optional TCP API listener.
// It blocks until cancellation or a listener/startup failure.
func (s *Server) Run(ctx context.Context) error {
	if s.opts.Addr != "" && !s.opts.AllowAnyBind && !validLoopback(s.opts.Addr) {
		return fmt.Errorf("juex serve: --addr must bind to loopback (got %q)", s.opts.Addr)
	}
	if err := app.EnsureActivePrimarySessionRecord(s.opts.Cfg); err != nil {
		return err
	}

	agentDir := s.opts.Cfg.RuntimePaths().StateDir
	if agentDir == "" {
		return errors.New("juex serve: agent state directory is empty")
	}
	binding, err := endpoint.Listen(ctx, agentDir, version.Version)
	if err != nil {
		return err
	}
	defer func() { _ = binding.Close() }()
	shutdownCh := s.setEndpointControl(binding.Runtime())
	defer s.clearEndpointControl(binding.Runtime())

	servers := []httpServerBinding{{
		server:   newHTTPServer(s.APIHandler()),
		listener: binding.Listener(),
	}}
	tcpAddress := ""
	if s.opts.Addr != "" {
		tcpListener, err := net.Listen("tcp", s.opts.Addr)
		if err != nil {
			return err
		}
		tcpAddress = tcpListener.Addr().String()
		servers = append(servers, httpServerBinding{
			server:   newHTTPServer(s.Handler()),
			listener: tcpListener,
		})
	}

	errCh := make(chan error, len(servers))
	for _, running := range servers {
		running := running
		go func() {
			if err := running.server.Serve(running.listener); !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}
	if err := binding.Publish(); err != nil {
		s.Close()
		_ = shutdownHTTPServers(servers, 10*time.Second)
		return err
	}
	if s.opts.OnReady != nil {
		info := ReadyInfo{
			AgentEndpoint: binding.Runtime().Endpoint,
			TCPAddress:    tcpAddress,
		}
		if binding.FallbackReason() != nil {
			info.FallbackReason = binding.FallbackReason().Error()
		}
		s.opts.OnReady(info)
	}

	startupCtx, cancelStartup := context.WithCancel(ctx)
	defer cancelStartup()
	startupErrCh := make(chan error, 1)
	startupDone := make(chan struct{})
	go func() {
		defer close(startupDone)
		// Keep warmup behind both listeners so startup notifications cannot hide readiness.
		if err := s.ensureMCPStarted(startupCtx); err != nil {
			startupErrCh <- err
			return
		}
		if err := s.ensureActivePrimarySession(startupCtx); err != nil {
			startupErrCh <- err
		}
	}()
	var runErr error
	select {
	case <-ctx.Done():
	case <-shutdownCh:
	case err := <-errCh:
		runErr = err
	case err := <-startupErrCh:
		if ctx.Err() == nil {
			runErr = err
		}
	}
	cancelStartup()
	s.Close()
	shutdownErr := shutdownHTTPServers(servers, 10*time.Second)
	waitForStartup(startupDone, 10*time.Second)
	return errors.Join(runErr, shutdownErr)
}

type httpServerBinding struct {
	server   *http.Server
	listener net.Listener
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func shutdownHTTPServers(servers []httpServerBinding, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var errs []error
	for _, running := range servers {
		if err := running.server.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func waitForStartup(done <-chan struct{}, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func (as *activeSession) cancelWork() {
	if as == nil || as.workCancel == nil {
		return
	}
	as.workCancel()
}

func (as *activeSession) close() {
	if as == nil {
		return
	}
	as.closeOnce.Do(func() {
		as.cancelWork()
		if as.statusUnsubscribe != nil {
			as.statusUnsubscribe()
			as.statusUnsubscribe = nil
		}
		if as.turns != nil {
			as.turns.close()
		}
		as.workWG.Wait()
		if as.bcast != nil {
			as.bcast.close()
		}
		if as.app != nil {
			_ = as.app.CloseAndWait()
		}
	})
}

// workContext ties server-origin work, such as MCP notifications, to session shutdown.
func (as *activeSession) workContext(parent context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if as != nil && as.workCtx != nil {
		base = as.workCtx
	}
	ctx, cancel := context.WithCancel(base)
	if parent == nil {
		return ctx, cancel
	}
	if err := parent.Err(); err != nil {
		cancel()
		return ctx, cancel
	}
	stop := context.AfterFunc(parent, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// Close cancels running turns and releases every active session.
func (s *Server) Close() {
	s.createMu.Lock()
	defer s.createMu.Unlock()

	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return
	}
	s.closed = true
	s.closeMu.Unlock()
	s.sessions.Range(func(_, v any) bool {
		v.(*activeSession).cancelWork()
		return true
	})
	s.closeMCPManager()
	s.sessions.Range(func(_, v any) bool {
		v.(*activeSession).close()
		return true
	})
}

func (s *Server) closeActiveSession(id string) bool {
	v, ok := s.sessions.LoadAndDelete(id)
	if !ok {
		return false
	}
	v.(*activeSession).close()
	s.statusHub.publish(s.agentActivity())
	return true
}

func (s *Server) closeOtherPrimarySessions(activeID string) {
	var ids []string
	s.sessions.Range(func(key, value any) bool {
		id, _ := key.(string)
		as, _ := value.(*activeSession)
		if id == "" || id == activeID || as == nil || as.app == nil {
			return true
		}
		identity, ok := as.app.SessionIdentity()
		if ok && session.NormalizeKind(identity.Kind) == session.KindPrimary {
			ids = append(ids, id)
		}
		return true
	})
	for _, id := range ids {
		s.closeActiveSession(id)
	}
}

// validLoopback accepts localhost or any loopback IP with an explicit port.
// The CLI surfaces a usage error before Run is called, but defending in depth
// here protects programmatic callers.
func validLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// openSession constructs an *app.App for resumeDir (or a fresh session
// when resumeDir == "") and stores it under its session id.
//
// For the resume path (resumeDir != "") we re-check the sessions map
// under createMu so two concurrent first-touches of the same on-disk
// session collapse to a single *app.App. The fresh-create path
// (resumeDir == "") doesn't need the re-check: app.New allocates a new
// id every call, so concurrent fresh creates produce distinct sessions.
func (s *Server) openSession(ctx context.Context, resumeDir string, mode app.SessionMode) (*activeSession, error) {
	if err := s.ensureMCPStarted(ctx); err != nil {
		return nil, err
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if s.isClosed() {
		return nil, context.Canceled
	}
	if resumeDir != "" {
		id := filepath.Base(resumeDir)
		if v, ok := s.sessions.Load(id); ok {
			return v.(*activeSession), nil
		}
	}
	a, err := app.New(app.Options{
		Config:      s.opts.Cfg,
		Provider:    s.opts.Provider,
		ModelHealth: s.modelHealth,
		Verbose:     s.opts.Verbose,
		Debug:       s.opts.Debug,
		LogLevel:    s.opts.LogLevel,
		Stderr:      s.stderr(),
		WorkDir:     s.opts.Cfg.WorkDir,
		MCPManager:  s.mcpManagerSnapshot(),
		DisableMCP:  true,
		ResumeDir:   resumeDir,
		SessionMode: mode,
		// A fresh web session should not write transcript files until
		// the first message; history.active is recorded immediately.
		LazySession: resumeDir == "",
	})
	if err != nil {
		s.recordMCPError(err)
		s.logVerbose("juex serve: open session failed: %v", err)
		return nil, err
	}
	workCtx, workCancel := context.WithCancel(context.Background())
	as := &activeSession{
		app:        a,
		bcast:      newBroadcaster(),
		StartedAt:  time.Now(),
		workCtx:    workCtx,
		workCancel: workCancel,
	}
	as.turns = newWebTurnTransport(a)
	a.AddEventDelivery(as.bcast)
	identity, ok := a.SessionIdentity()
	if !ok {
		_ = a.CloseAndWait()
		return nil, app.ErrSessionUnavailable
	}
	s.sessions.Store(identity.ID, as)
	if a.Status != nil {
		snapshot := a.Status.Snapshot()
		subscription := a.Status.SubscribeFrom(snapshot.Cursor)
		as.statusUnsubscribe = subscription.Unsubscribe
		as.workWG.Add(1)
		go func() {
			defer as.workWG.Done()
			for {
				select {
				case _, ok := <-subscription.Updates:
					if !ok {
						return
					}
					s.statusHub.publish(s.agentActivity())
				case <-as.workCtx.Done():
					return
				}
			}
		}()
	}
	s.statusHub.publish(s.agentActivity())
	if session.NormalizeKind(identity.Kind) == session.KindPrimary {
		s.closeOtherPrimarySessions(identity.ID)
	}
	return as, nil
}

func (s *Server) ensureActivePrimarySession(ctx context.Context) error {
	id, ok, err := s.activePrimarySessionID()
	if err != nil {
		return err
	}
	if !ok || !s.hasSessionProvider() {
		return nil
	}
	if _, exists := s.sessions.Load(id); exists {
		return nil
	}
	_, err = s.getActiveSession(ctx, id)
	return err
}

func (s *Server) hasSessionProvider() bool {
	return s.opts.Provider != nil || s.opts.Cfg.ProviderID != "" || s.opts.Cfg.ProviderProtocol != ""
}

func (s *Server) ensureMCPStarted(ctx context.Context) (err error) {
	s.mcpMu.Lock()
	if s.mcpStarted {
		starting := s.mcpStarting
		s.mcpMu.Unlock()
		if starting == nil {
			return nil
		}
		select {
		case <-starting:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mcpMu.Lock()
		defer s.mcpMu.Unlock()
		return s.mcpStartErr
	}
	s.mcpStarted = true
	s.mcpStartErr = nil
	starting := make(chan struct{})
	s.mcpStarting = starting
	s.mcpMu.Unlock()
	startupFinished := false
	finishStartup := func(startErr error) {
		s.mcpMu.Lock()
		if startErr != nil {
			s.mcpStarted = false
		}
		s.mcpStartErr = startErr
		s.mcpStarting = nil
		s.mcpMu.Unlock()
		close(starting)
	}
	defer func() {
		if !startupFinished {
			finishStartup(err)
		}
	}()

	mcpConfigs, err := s.loadMCPConfigs()
	if err != nil {
		return err
	}
	if len(mcpConfigs) == 0 {
		return nil
	}
	var ready atomic.Bool
	var queuedMu sync.Mutex
	var queued []mcp.Notification
	handleNotification := func(n mcp.Notification) {
		if !ready.Load() {
			queuedMu.Lock()
			queued = append(queued, n)
			queuedMu.Unlock()
			return
		}
		if err := s.handleMCPNotification(context.Background(), n); err != nil {
			s.logVerbose("juex serve: MCP notification dropped: %v", err)
		}
	}
	mgr, err := mcp.NewManagerLayeredSoft(ctx, mcpConfigs, mcp.ConnectOptions{
		OnNotification:      handleNotification,
		EnableClaudeChannel: true,
	})
	if err != nil {
		s.recordMCPError(err)
		s.logVerbose("juex serve: MCP startup failed: %v", err)
		return nil
	}
	s.setMCPErrors(mgr.StartupErrors())

	s.mcpMu.Lock()
	if s.isClosed() {
		s.mcpMu.Unlock()
		if err := mgr.Close(); err != nil {
			s.logVerbose("juex serve: MCP shutdown failed: %v", err)
		}
		return nil
	}
	s.mcpManager = mgr
	s.mcpMu.Unlock()
	ready.Store(true)
	finishStartup(nil)
	startupFinished = true
	queuedMu.Lock()
	pending := append([]mcp.Notification(nil), queued...)
	queued = nil
	queuedMu.Unlock()
	for _, n := range pending {
		handleNotification(n)
	}
	return nil
}

func (s *Server) mcpManagerSnapshot() *mcp.Manager {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	return s.mcpManager
}

func (s *Server) mcpToolDescriptors() map[string][]mcp.ToolDescriptor {
	mgr := s.mcpManagerSnapshot()
	if mgr == nil {
		return map[string][]mcp.ToolDescriptor{}
	}
	return mgr.ToolDescriptors()
}

func (s *Server) closeMCPManager() {
	s.mcpMu.Lock()
	mgr := s.mcpManager
	s.mcpManager = nil
	s.mcpMu.Unlock()
	if mgr != nil {
		_ = mgr.Close()
	}
}

func (s *Server) isClosed() bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closed
}

func (s *Server) handleMCPNotification(ctx context.Context, n mcp.Notification) error {
	id, ok, err := s.activePrimarySessionID()
	if err != nil {
		return err
	}
	if !ok {
		s.logVerbose("juex serve: MCP notification dropped: no active primary session")
		return nil
	}
	as, err := s.getActiveSession(ctx, id)
	if err != nil {
		return err
	}
	s.createMu.Lock()
	if s.isClosed() {
		s.createMu.Unlock()
		return context.Canceled
	}
	workCtx, cancel := as.workContext(ctx)
	as.workWG.Add(1)
	s.createMu.Unlock()
	defer as.workWG.Done()
	defer cancel()
	return as.app.HandleMCPNotification(workCtx, n)
}

func (s *Server) activePrimarySessionID() (string, bool, error) {
	return app.ActivePrimarySessionID(s.opts.Cfg)
}

func (s *Server) stderr() io.Writer {
	if s.opts.Stderr != nil {
		return s.opts.Stderr
	}
	return os.Stderr
}

func (s *Server) logVerbose(format string, args ...any) {
	if !s.opts.Verbose {
		return
	}
	fmt.Fprintf(s.stderr(), format+"\n", args...)
}

func (s *Server) recordMCPError(err error) {
	name, ok := mcp.ErrorServerName(err)
	if !ok {
		return
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeMCPErr == nil {
		s.runtimeMCPErr = map[string]string{}
	}
	s.runtimeMCPErr[name] = err.Error()
}

func (s *Server) setMCPErrors(errors map[string]string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.runtimeMCPErr = map[string]string{}
	for name, msg := range errors {
		if msg != "" {
			s.runtimeMCPErr[name] = msg
		}
	}
}

func (s *Server) mcpErrors() map[string]string {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	out := make(map[string]string, len(s.runtimeMCPErr))
	for name, msg := range s.runtimeMCPErr {
		out[name] = msg
	}
	return out
}

// getActiveSession returns the active session for id; opens it from
// disk if not already in memory. Returns nil if the on-disk dir is
// missing.
func (s *Server) getActiveSession(ctx context.Context, id string) (*activeSession, error) {
	if v, ok := s.sessions.Load(id); ok {
		as := v.(*activeSession)
		if activeSessionMatches(as, id) {
			return as, nil
		}
		return nil, os.ErrNotExist
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	if !session.HasConversation(dir) {
		return nil, os.ErrNotExist
	}
	return s.openSession(ctx, dir, app.SessionModeAttachActive)
}

func activeSessionMatches(as *activeSession, id string) bool {
	if as == nil || as.app == nil || id == "" {
		return false
	}
	identity, ok := as.app.SessionIdentity()
	return ok && identity.ID == id
}
