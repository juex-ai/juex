package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

// Options configures a Server. Provider is optional; if unset, the
// server constructs one per-session via cfg.NewProvider().
type Options struct {
	Cfg          config.Config
	Addr         string
	Provider     llm.Provider // optional; injected for tests
	AllowAnyBind bool         // bypass the loopback bind check; CLI sets this for --unsafe-bind-any
}

// Server is a long-running HTTP server for one WorkDir.
type Server struct {
	opts     Options
	sessions sync.Map // session id (string) → *activeSession
	nextTurn atomic.Uint64

	createMu sync.Mutex // serialises POST /api/sessions
	closeMu  sync.Mutex
	closed   bool
}

// activeSession wraps an app.App with the bookkeeping the web server
// needs for SSE fan-out and turn cancellation.
type activeSession struct {
	app       *app.App
	bcast     *broadcaster
	StartedAt time.Time

	cancelMu sync.Mutex
	cancel   context.CancelFunc // nil when no turn is running
	turnWG   sync.WaitGroup

	turnsMu sync.Mutex
	turns   map[string]*turnState
}

type turnState struct {
	ID    string
	State string // "running" | "done" | "errored"
	Err   string
}

func NewServer(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8080"
	}
	return &Server{opts: opts}
}

// Handler returns the http.Handler wired with every route. Exposed so
// tests can mount it under httptest without spinning a real listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
}

// registerRoutes wires every URL pattern. Subsequent tasks add more
// handlers; for now /healthz is enough.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/api/sessions", s.handleListSessions)
	mux.HandleFunc("/api/sessions/", s.dispatchSession)
	mux.HandleFunc("/api/files/tree", s.handleFilesTree)
	mux.HandleFunc("/api/files/content", s.handleFilesContent)
	// SPA: anything else is the React app.
	spa := spaHandler()
	mux.Handle("/", spa)
	mux.Handle("/sessions/", spa)
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
	case strings.HasPrefix(rest, "turns/") && r.Method == http.MethodGet:
		s.handleTurnStatus(w, r, id, strings.TrimPrefix(rest, "turns/"))
	case rest == "turns" && r.Method == http.MethodPost:
		s.handleStartTurn(w, r, id)
	case rest == "interrupt" && r.Method == http.MethodPost:
		s.handleInterrupt(w, r, id)
	case rest == "events" && r.Method == http.MethodGet:
		s.handleEventsSSE(w, r, id)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method or sub-path")
	}
}

// Run starts the HTTP server and blocks until ctx is cancelled. On
// shutdown it cancels every running turn, closes every active app, and
// then calls http.Server.Shutdown with a 10s deadline.
func (s *Server) Run(ctx context.Context) error {
	if !s.opts.AllowAnyBind && !validLoopback(s.opts.Addr) {
		return fmt.Errorf("juex serve: --addr must bind to loopback (got %q)", s.opts.Addr)
	}
	srv := &http.Server{
		Addr:              s.opts.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.Close()
	return srv.Shutdown(shutdownCtx)
}

// Close cancels running turns and releases every active session.
func (s *Server) Close() {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return
	}
	s.closed = true
	s.closeMu.Unlock()
	s.sessions.Range(func(_, v any) bool {
		as := v.(*activeSession)
		as.cancelMu.Lock()
		if as.cancel != nil {
			as.cancel()
		}
		as.cancelMu.Unlock()
		as.turnWG.Wait()
		as.bcast.close()
		as.app.Close()
		return true
	})
}

func (s *Server) closeActiveSession(id string) {
	v, ok := s.sessions.LoadAndDelete(id)
	if !ok {
		return
	}
	as := v.(*activeSession)
	as.cancelMu.Lock()
	if as.cancel != nil {
		as.cancel()
	}
	as.cancelMu.Unlock()
	as.turnWG.Wait()
	as.bcast.close()
	as.app.Close()
}

// validLoopback enforces "127.0.0.1" / "::1" / "localhost" hosts. The
// CLI surfaces a usage error before Run is called, but defending in
// depth here protects programmatic callers.
func validLoopback(addr string) bool {
	for _, prefix := range []string{"127.0.0.1:", "[::1]:", "localhost:"} {
		if len(addr) >= len(prefix) && addr[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// openSession constructs an *app.App for resumeDir (or a fresh session
// when resumeDir == "") and stores it under its session id.
//
// For the resume path (resumeDir != "") we re-check the sessions map
// under createMu so two concurrent first-touches of the same on-disk
// session collapse to a single *app.App. The fresh-create path
// (resumeDir == "") doesn't need the re-check: app.New allocates a new
// id every call, so concurrent fresh creates produce distinct sessions.
func (s *Server) openSession(ctx context.Context, resumeDir string) (*activeSession, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if resumeDir != "" {
		id := filepath.Base(resumeDir)
		if v, ok := s.sessions.Load(id); ok {
			return v.(*activeSession), nil
		}
	}
	a, err := app.New(app.Options{
		Config:    s.opts.Cfg,
		Provider:  s.opts.Provider,
		WorkDir:   s.opts.Cfg.WorkDir,
		ResumeDir: resumeDir,
	})
	if err != nil {
		return nil, err
	}
	as := &activeSession{
		app:       a,
		bcast:     newBroadcaster(),
		StartedAt: time.Now(),
		turns:     map[string]*turnState{},
	}
	a.Bus.Subscribe("*", func(e events.Event) { as.bcast.publish(e) })
	s.sessions.Store(a.Session.ID, as)
	return as, nil
}

// getActiveSession returns the active session for id; opens it from
// disk if not already in memory. Returns nil if the on-disk dir is
// missing.
func (s *Server) getActiveSession(ctx context.Context, id string) (*activeSession, error) {
	if v, ok := s.sessions.Load(id); ok {
		return v.(*activeSession), nil
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	if _, err := os.Stat(filepath.Join(dir, "conversation.jsonl")); err != nil {
		return nil, err
	}
	return s.openSession(ctx, dir)
}
