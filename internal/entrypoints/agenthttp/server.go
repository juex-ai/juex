package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	agentsmdmodule "github.com/juex-ai/juex/internal/features/agentsmd"
	observablesmodule "github.com/juex-ai/juex/internal/features/observables"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/version"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/endpoint"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/runtime"
	statusapi "github.com/juex-ai/juex/internal/framework/status"
	"github.com/juex-ai/juex/internal/framework/thread"
)

// Options configures a Server. Provider is optional; if unset, each Thread
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
	inspectionDone      chan struct{}
	readOnly            bool
	inspectionFactories []runtimemodule.ThreadFactorySpec
	opts                Options
	threads             sync.Map // Thread id (string) → *activeThread
	startedAt           time.Time
	statusStream        *statusapi.ActivityStore
	resources           *resourceEventHub

	// createMu serializes live Thread creation and restoration.
	createMu        sync.Mutex
	closeMu         sync.Mutex
	closed          bool
	deferredCloseWG sync.WaitGroup

	servicesOnce    sync.Once
	services        *app.ProcessServices
	threadIndexOnce sync.Once
	threadIndexMu   sync.Mutex
	threadIndexErr  error

	endpointMu       sync.RWMutex
	endpointRuntime  endpoint.Runtime
	endpointShutdown chan struct{}
}

// activeThread wraps an Agent with the bookkeeping the web server
// needs for SSE fan-out and turn cancellation.
type activeThread struct {
	agent     *agent.Agent
	main      *app.App
	ownsAgent bool
	bcast     *broadcaster
	StartedAt time.Time

	turns             *webTurnTransport
	statusStreamClose func()
	workCtx           context.Context
	workCancel        context.CancelFunc
	workWG            sync.WaitGroup
	closeOnce         sync.Once
}

var errThreadInactive = errors.New("web: Thread is archived")

func NewServer(opts Options) *Server {
	observablesPath := ""
	if opts.Cfg.ModuleEnabled(observablesmodule.ModuleID) {
		observablesPath = opts.Cfg.ObservablesConfigPath()
	}
	resources := newResourceEventHub(opts.Cfg.WorkDir, observablesPath)
	resources.setRuntimeInputs([]string{opts.Cfg.ThreadIndexPath()})
	if opts.Cfg.ModuleEnabled(agentsmdmodule.ModuleID) {
		resources.setRuntimeInputs([]string{opts.Cfg.GlobalAgentsMDPath()})
	}
	return &Server{
		inspectionDone: make(chan struct{}),
		opts:           opts,
		startedAt:      time.Now().UTC(),
		statusStream:   statusapi.NewActivityStore(),
		resources:      resources,
	}
}

// Handler returns the TCP agent API with a pointer for non-API browser routes.
func (s *Server) Handler() http.Handler {
	s.prepareThreadIndex()
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)
	mux.HandleFunc("/", s.handleAgentAPIPointer)
	return mux
}

// APIHandler returns the canonical local agent API without a browser fallback.
func (s *Server) APIHandler() http.Handler {
	s.prepareThreadIndex()
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)
	return mux
}

// NewReadOnlyAPIHandler serves persisted Thread data without starting an agent
// runtime. It intentionally exposes only the durable GET endpoints needed to
// inspect stopped agents through the fleet UI.
func NewReadOnlyAPIHandler(cfg config.Config) http.Handler {
	server := NewServer(Options{Cfg: cfg})
	server.readOnly = true
	mux := http.NewServeMux()
	mux.HandleFunc("/api/threads", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		server.listThreads(w)
	})
	mux.HandleFunc("/api/threads/", server.dispatchReadOnlyThread)
	mux.HandleFunc("/api/files/content", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("root") != "artifact" {
			writeErr(w, http.StatusNotFound, "not_found", "read-only API route not found")
			return
		}
		server.handleFilesContent(w, r)
	})
	mux.HandleFunc("/api/media", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or HEAD required")
			return
		}
		server.handleMedia(w, r)
	})
	return mux
}

func (s *Server) prepareThreadIndex() {
	s.threadIndexOnce.Do(func() {
		stateDir := s.opts.Cfg.RuntimePaths().StateDir
		if stateDir != "" {
			s.threadIndexErr = thread.NewStore(stateDir).RecoverLayout()
		}
	})
}

func (s *Server) ensureThreadIndexReady() error {
	s.prepareThreadIndex()
	s.threadIndexMu.Lock()
	defer s.threadIndexMu.Unlock()
	if s.threadIndexErr == nil {
		return nil
	}
	stateDir := s.opts.Cfg.RuntimePaths().StateDir
	if stateDir == "" {
		return s.threadIndexErr
	}
	s.threadIndexErr = thread.NewStore(stateDir).RecoverLayout()
	return s.threadIndexErr
}

func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/api/identity", s.handleEndpointIdentity)
	mux.HandleFunc("/api/control/shutdown", s.handleEndpointShutdown)
	mux.HandleFunc("/api/threads", s.handleListThreads)
	mux.HandleFunc("/api/threads/", s.dispatchThread)
	mux.HandleFunc("/api/files/tree", s.handleFilesTree)
	mux.HandleFunc("/api/files/content", s.handleFilesContent)
	mux.HandleFunc("/api/files/raw", s.handleFilesRaw)
	mux.HandleFunc("/api/media", s.handleMedia)
	mux.HandleFunc("/api/status", s.handleAgentStatus)
	mux.HandleFunc("/api/status/events", s.handleAgentStatusEvents)
	mux.HandleFunc("/api/resource-events", s.handleResourceEvents)
	mux.HandleFunc("/api/runtime", s.handleRuntimeStatus)
	mux.HandleFunc("/api/observables", s.handleObservables)
	mux.HandleFunc("/api/observables/", s.dispatchObservable)
}

func (s *Server) dispatchReadOnlyThread(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	id, rest := threadPathID(r.URL.Path)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing Thread id")
		return
	}
	if rest == "modules" || strings.HasPrefix(rest, "modules/") {
		s.dispatchModuleInspection(w, r, id, rest)
		return
	}
	switch rest {
	case "":
		s.handleThreadShow(w, r, id)
	case "context":
		s.handleThreadContext(w, r, id)
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
	fmt.Fprintln(w, "This endpoint serves one agent through the agent JSON/SSE API (no web UI).")
	fmt.Fprintln(w, "API routes are available under /api/.")
	fleetAddr := strings.TrimSpace(s.opts.Cfg.Fleet.Addr)
	if fleetAddr == "" {
		fleetAddr = config.DefaultFleetAddr
	}
	fmt.Fprintf(w, "For a browser UI covering all registered agents, run `juex fleet serve` and open http://%s/.\n", fleetAddr)
}

// dispatchThread routes /api/threads/<id>[/...] to the matching handler.
func (s *Server) dispatchThread(w http.ResponseWriter, r *http.Request) {
	id, rest := threadPathID(r.URL.Path)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing Thread id")
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		s.handleThreadShow(w, r, id)
	case rest == "" && r.Method == http.MethodDelete:
		s.handleDeleteThread(w, r, id)
	case rest == "archive" && r.Method == http.MethodPost:
		s.handleArchiveThread(w, r, id)
	case rest == "unarchive" && r.Method == http.MethodPost:
		s.handleUnarchiveThread(w, r, id)
	case rest == "" && r.Method == http.MethodPatch:
		s.handleRenameThread(w, r, id)
	case rest == "inputs" && r.Method == http.MethodPost:
		s.handleStartTurn(w, r, id)
	case rest == "attachments" && r.Method == http.MethodPost:
		s.handleThreadAttachmentUpload(w, r, id)
	case rest == "stop" && r.Method == http.MethodPost:
		s.handleInterrupt(w, r, id)
	case rest == "events" && r.Method == http.MethodGet:
		s.handleEventsSSE(w, r, id)
	case rest == "status" && r.Method == http.MethodGet:
		s.handleThreadStatus(w, r, id)
	case rest == "status/events" && r.Method == http.MethodGet:
		s.handleThreadStatusEvents(w, r, id)
	case rest == "compact" && r.Method == http.MethodPost:
		s.handleCompactThread(w, r, id)
	case rest == "context" && r.Method == http.MethodGet:
		s.handleThreadContext(w, r, id)
	case rest == "modules" || strings.HasPrefix(rest, "modules/"):
		s.dispatchModuleInspection(w, r, id, rest)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method or sub-path")
	}
}

// Run starts the canonical agent API endpoint and an optional TCP API listener.
// It blocks until cancellation or a listener/startup failure.
func (s *Server) Run(ctx context.Context) error {
	if err := app.ValidateModuleConfig(s.opts.Cfg); err != nil {
		return err
	}
	if s.opts.Addr != "" && !s.opts.AllowAnyBind && !validLoopback(s.opts.Addr) {
		return fmt.Errorf("juex listen: --addr must bind to loopback (got %q)", s.opts.Addr)
	}

	address := s.opts.Cfg.AgentAddress
	if address.ID() == "" {
		return errors.New("juex listen: agent address is empty")
	}
	binding, err := endpoint.Listen(ctx, address, version.Version)
	if err != nil {
		return err
	}
	defer func() { _ = binding.Close() }()
	if _, err := s.processServices().RuntimeResolution(); err != nil {
		return err
	}
	resourceLease, err := app.AcquireModuleResources(s.opts.Cfg)
	if err != nil {
		return err
	}
	defer func() { _ = resourceLease.Close() }()
	if err := agent.EnsureMainThread(s.opts.Cfg.RuntimePaths().StateDir); err != nil {
		return err
	}
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
		if err := s.processServices().EnsureMCPStarted(startupCtx); err != nil {
			startupErrCh <- err
			return
		}
		if err := s.ensureMainThread(startupCtx); err != nil {
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

func (as *activeThread) cancelWork() {
	if as == nil || as.workCancel == nil {
		return
	}
	as.workCancel()
}

func (as *activeThread) beginClose() {
	if as == nil {
		return
	}
	as.cancelWork()
	if as.turns != nil {
		as.turns.interruptWithCause(agent.ErrThreadStopped)
	}
	if as.agent != nil && as.ownsAgent {
		_ = as.agent.BeginClose()
	}
}

func (as *activeThread) close() {
	if as == nil {
		return
	}
	as.closeOnce.Do(func() {
		as.beginClose()
		if as.statusStreamClose != nil {
			as.statusStreamClose()
			as.statusStreamClose = nil
		}
		if as.turns != nil {
			as.turns.close()
		}
		as.workWG.Wait()
		if as.bcast != nil {
			as.bcast.close()
		}
		if as.agent != nil && as.ownsAgent {
			_ = as.agent.CloseAndWait()
		}
	})
}

// workContext ties server-origin work, such as MCP notifications, to Thread shutdown.
func (as *activeThread) workContext(parent context.Context) (context.Context, context.CancelFunc) {
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

// Close cancels running turns and releases every active Thread.
func (s *Server) Close() {
	s.createMu.Lock()
	defer s.createMu.Unlock()

	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return
	}
	s.closed = true
	close(s.inspectionDone)
	s.closeMu.Unlock()
	s.threads.Range(func(_, v any) bool {
		v.(*activeThread).beginClose()
		return true
	})
	s.processServices().Close()
	s.resources.close()
	s.threads.Range(func(_, v any) bool {
		v.(*activeThread).close()
		return true
	})
	s.deferredCloseWG.Wait()
}

func (s *Server) deferCloseActiveThread(id string) (*activeThread, bool) {
	v, ok := s.threads.LoadAndDelete(id)
	if !ok {
		return nil, false
	}
	as := v.(*activeThread)
	s.deferCloseThread(as)
	s.statusStream.Publish(s.agentActivity())
	return as, true
}

func (s *Server) deferCloseThread(as *activeThread) {
	if as == nil {
		return
	}
	as.beginClose()
	s.deferredCloseWG.Add(1)
	go func() {
		defer s.deferredCloseWG.Done()
		as.close()
	}()
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

// openThread restores one active Thread runtime. Concurrent first touches of
// the same Thread collapse to a single App instance.
func (s *Server) openThread(ctx context.Context, id string) (*activeThread, error) {
	if err := s.processServices().EnsureMCPStarted(ctx); err != nil {
		return nil, err
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	return s.openThreadLocked(ctx, id)
}

func (s *Server) openThreadLocked(ctx context.Context, id string) (*activeThread, error) {
	if s.isClosed() {
		return nil, context.Canceled
	}
	if !thread.ValidID(id) {
		return nil, os.ErrNotExist
	}
	if value, ok := s.threads.Load(id); ok {
		active := value.(*activeThread)
		if active.ownsAgent || s.isManagedWorkerAgent(id, active.agent) {
			return active, nil
		}
		s.threads.Delete(id)
		active.close()
	}
	resourceLease, err := app.AcquireModuleResources(s.opts.Cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resourceLease.Close() }()
	store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
	probe, err := store.OpenActive(id)
	if os.IsNotExist(err) && id == thread.MainID {
		probe, err = store.EnsureMain()
	}
	if err != nil {
		if archived, archivedErr := store.OpenArchived(id); archivedErr == nil {
			_ = archived.Close()
			return nil, errThreadInactive
		}
		return nil, err
	}
	_ = probe.Close()
	if managed := s.managedWorkerAgent(id); managed != nil {
		return s.bindThreadAgent(managed, nil, false)
	}
	a, err := s.processServices().NewThread(id)
	if err != nil {
		s.logVerbose("juex listen: open Thread failed: %v", err)
		return nil, err
	}
	return s.bindThreadAgent(a.Agent, a, true)
}

func (s *Server) managedWorkerAgent(id string) *agent.Agent {
	value, ok := s.threads.Load(thread.MainID)
	if !ok {
		return nil
	}
	worker, ok := value.(*activeThread).agent.ManagedWorkerAgent(id)
	if !ok {
		return nil
	}
	return worker
}

func (s *Server) isManagedWorkerAgent(id string, candidate *agent.Agent) bool {
	return candidate != nil && s.managedWorkerAgent(id) == candidate
}

func (s *Server) bindThreadAgent(a *agent.Agent, main *app.App, ownsAgent bool) (*activeThread, error) {
	workCtx, workCancel := context.WithCancel(context.Background())
	as := &activeThread{
		agent:      a,
		main:       main,
		ownsAgent:  ownsAgent,
		bcast:      newBroadcaster(),
		StartedAt:  time.Now(),
		workCtx:    workCtx,
		workCancel: workCancel,
	}
	as.turns = newWebTurnTransport(a)
	a.AddEventProjection(s.resources)
	a.AddEventProjection(browserEventProjection{
		status: a.Status,
		stream: as.bcast,
	})
	identity, ok := a.ThreadIdentity()
	if !ok {
		if ownsAgent {
			_ = a.CloseAndWait()
		}
		return nil, agent.ErrThreadUnavailable
	}
	s.threads.Store(identity.ID, as)
	if a.Status != nil {
		snapshot := a.Status.Snapshot()
		stream := a.Status.OpenStream(runtime.StatusStreamOptions{
			After:  snapshot.Cursor,
			Follow: true,
		})
		as.statusStreamClose = stream.Close
		as.workWG.Add(1)
		go func() {
			defer as.workWG.Done()
			first := true
			for {
				if _, ok := stream.Next(as.workCtx); !ok {
					return
				}
				if first {
					first = false
					continue
				}
				s.statusStream.Publish(s.agentActivity())
			}
		}()
	}
	s.statusStream.Publish(s.agentActivity())
	return as, nil
}

func (s *Server) ensureMainThread(ctx context.Context) error {
	if !s.hasThreadProvider() {
		return nil
	}
	_, err := s.getThread(ctx, thread.MainID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Server) hasThreadProvider() bool {
	return s.opts.Provider != nil || s.opts.Cfg.ProviderID != "" || s.opts.Cfg.ProviderProtocol != ""
}

func (s *Server) isClosed() bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closed
}

func (s *Server) withMain(ctx context.Context, use func(context.Context, *app.App) error) error {
	as, err := s.getThread(ctx, thread.MainID)
	if errors.Is(err, os.ErrNotExist) {
		s.logVerbose("juex listen: MCP notification dropped: Main Thread unavailable")
		return nil
	}
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
	return use(workCtx, as.main)
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
	message := fmt.Sprintf(format, args...)
	fmt.Fprintln(s.stderr(), s.processServices().RedactRuntimeText(message))
}

func (s *Server) getThread(ctx context.Context, id string) (*activeThread, error) {
	if err := s.processServices().EnsureMCPStarted(ctx); err != nil {
		return nil, err
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	return s.openThreadLocked(ctx, id)
}

func threadPathID(path string) (id, rest string) {
	const prefix = "/api/threads/"
	if !strings.HasPrefix(path, prefix) {
		return "", ""
	}
	tail := strings.TrimPrefix(path, prefix)
	if i := strings.IndexByte(tail, '/'); i >= 0 {
		return tail[:i], strings.Trim(tail[i+1:], "/")
	}
	return tail, ""
}

func (s *Server) processServices() *app.ProcessServices {
	s.servicesOnce.Do(func() {
		s.services = app.NewProcessServices(app.ProcessServicesOptions{
			Config: s.opts.Cfg, Provider: s.opts.Provider, Verbose: s.opts.Verbose, Debug: s.opts.Debug, LogLevel: s.opts.LogLevel, Stderr: s.opts.Stderr, WithMain: s.withMain,
		})
	})
	return s.services
}
