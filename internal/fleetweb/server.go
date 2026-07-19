// Package fleetweb exposes the fleet manager through a loopback browser API.
package fleetweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/web"
)

const (
	maxConfigRequestBytes        = 1 << 20
	maxAgentMutationRequestBytes = 64 << 10
)

type Options struct {
	Manager      *fleet.Manager
	Addr         string
	AllowAnyBind bool
	OnReady      func(string)
}

type backend interface {
	Status(context.Context) ([]fleet.AgentStatus, error)
	Add(context.Context, fleet.AddOptions) (fleet.AddResult, error)
	Start(context.Context, string) (fleet.AgentStatus, error)
	Stop(context.Context, string) (fleet.AgentStatus, error)
	Restart(context.Context, string) (fleet.AgentStatus, error)
	SetEnabled(context.Context, string, bool) (fleet.AgentStatus, error)
	Remove(context.Context, string, fleet.RemoveOptions) (fleet.RemovedAgent, error)
	Logs(string, int) ([]byte, error)
	Config(string) (fleet.AgentConfig, error)
	UpdateConfig(context.Context, string, []byte) (fleet.AgentConfig, fleet.AgentStatus, error)
	Endpoint(context.Context, string) (endpoint.Runtime, error)
}

type readOnlyStateBackend interface {
	ReadOnlyState(string) (fleet.ReadOnlyAgentState, error)
}

type Server struct {
	manager         backend
	addr            string
	allowAnyBind    bool
	onReady         func(string)
	spa             http.Handler
	readActivity    func(context.Context, fleet.AgentStatus) (*agentActivity, error)
	activityClients *activityClientPool
	fleetStatus     *fleetStatusHub
}

func New(opts Options) (*Server, error) {
	if opts.Manager == nil {
		return nil, errors.New("fleet web: manager is required")
	}
	return newServer(opts.Manager, opts), nil
}

func newServer(manager backend, opts Options) *Server {
	addr := strings.TrimSpace(opts.Addr)
	if addr == "" {
		addr = config.DefaultFleetAddr
	}
	activityClients := newActivityClientPool()
	server := &Server{
		manager:         manager,
		addr:            addr,
		allowAnyBind:    opts.AllowAnyBind,
		onReady:         opts.OnReady,
		spa:             web.SPAHandler(),
		readActivity:    activityClients.fetch,
		activityClients: activityClients,
	}
	server.fleetStatus = newFleetStatusHub(manager, activityClients)
	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/agents", s.handleAgents)
	mux.HandleFunc("/api/agents/", s.dispatchAgentAPI)
	mux.HandleFunc("/api/fleet/events", s.handleFleetEvents)
	mux.HandleFunc("/api/fs/dirs", s.handleDirectories)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "API route not found")
	})
	mux.HandleFunc("/agents/", s.dispatchAgentRoute)
	mux.Handle("/", s.spa)
	return mux
}

func (s *Server) Run(ctx context.Context) error {
	if s.activityClients != nil {
		defer s.activityClients.close()
	}
	if !s.allowAnyBind && !validLoopback(s.addr) {
		return fmt.Errorf("juex fleet serve: --addr must bind to loopback (got %q)", s.addr)
	}
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf(
			"juex fleet serve: listen on %s: %w; change fleet.addr in $JUEX_HOME/juex.yaml or free the port",
			s.addr,
			err,
		)
	}
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	if s.onReady != nil {
		s.onReady(listener.Addr().String())
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		statuses, err := s.manager.Status(r.Context())
		if err != nil {
			writeFleetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.roster(r.Context(), statuses))
	case http.MethodPost:
		var body struct {
			Workspace *string `json:"workspace"`
			Name      *string `json:"name"`
			Autostart *bool   `json:"autostart"`
			Start     bool    `json:"start"`
		}
		if !decodeJSONBody(
			w,
			r,
			maxAgentMutationRequestBytes,
			&body,
			"request body must describe an agent workspace",
		) {
			return
		}
		if body.Workspace == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "workspace is required")
			return
		}
		result, err := s.manager.Add(r.Context(), fleet.AddOptions{
			Workspace: *body.Workspace,
			Name:      body.Name,
			Autostart: body.Autostart,
			Start:     body.Start,
		})
		if err != nil {
			writeFleetError(w, err)
			return
		}
		status := http.StatusOK
		if result.Created {
			status = http.StatusCreated
		}
		writeJSON(w, status, result)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

func (s *Server) dispatchAgentAPI(w http.ResponseWriter, r *http.Request) {
	selector, action, ok := parseAgentAPIPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "fleet API route not found")
		return
	}
	switch action {
	case "":
		s.handleRemove(w, r, selector)
	case "start", "stop", "restart":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}
		var (
			status fleet.AgentStatus
			err    error
		)
		switch action {
		case "start":
			status, err = s.manager.Start(r.Context(), selector)
		case "stop":
			status, err = s.manager.Stop(r.Context(), selector)
		case "restart":
			status, err = s.manager.Restart(r.Context(), selector)
		}
		if err != nil {
			writeFleetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	case "enable", "disable":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}
		status, err := s.manager.SetEnabled(r.Context(), selector, action == "enable")
		if err != nil {
			writeFleetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	case "logs":
		s.handleLogs(w, r, selector)
	case "config":
		s.handleConfig(w, r, selector)
	default:
		writeError(w, http.StatusNotFound, "not_found", "fleet API route not found")
	}
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request, selector string) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}
	var body struct {
		Confirm *string `json:"confirm"`
	}
	if !decodeJSONBody(
		w,
		r,
		maxAgentMutationRequestBytes,
		&body,
		"request body must contain an exact agent-name confirmation",
	) {
		return
	}
	if body.Confirm == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "confirm is required")
		return
	}
	removed, err := s.manager.Remove(r.Context(), selector, fleet.RemoveOptions{
		ConfirmName: *body.Confirm,
	})
	if err != nil {
		writeFleetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, removed)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, selector string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	lines := 200
	if raw := r.URL.Query().Get("lines"); raw != "" {
		var err error
		lines, err = strconv.Atoi(raw)
		if err != nil || lines < 1 || lines > 10_000 {
			writeError(w, http.StatusBadRequest, "bad_request", "lines must be between 1 and 10000")
			return
		}
	}
	content, err := s.manager.Logs(selector, lines)
	if err != nil {
		writeFleetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Content string `json:"content"`
	}{Content: string(content)})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request, selector string) {
	switch r.Method {
	case http.MethodGet:
		configState, err := s.manager.Config(selector)
		if err != nil {
			writeFleetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, configState)
	case http.MethodPut:
		var body struct {
			Content *string `json:"content"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxConfigRequestBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 1 MiB")
				return
			}
			writeError(w, http.StatusBadRequest, "bad_request", "request body must be JSON with a content string")
			return
		}
		if body.Content == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "content is required")
			return
		}
		if err := requireJSONEOF(decoder); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "request body must contain one JSON object")
			return
		}
		configState, status, err := s.manager.UpdateConfig(
			r.Context(),
			selector,
			[]byte(*body.Content),
		)
		if err != nil {
			writeFleetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Config fleet.AgentConfig `json:"config"`
			Agent  fleet.AgentStatus `json:"agent"`
		}{Config: configState, Agent: status})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or PUT required")
	}
}

func (s *Server) dispatchAgentRoute(w http.ResponseWriter, r *http.Request) {
	selector, upstreamPath, proxy := parseAgentProxyPath(r.URL.Path)
	if proxy {
		s.proxyAgent(w, r, selector, upstreamPath)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	s.spa.ServeHTTP(w, r)
}

func (s *Server) proxyAgent(w http.ResponseWriter, r *http.Request, selector, upstreamPath string) {
	runtimeState, err := s.manager.Endpoint(r.Context(), selector)
	if err != nil {
		if s.serveReadOnlyAgent(w, r, selector, upstreamPath) {
			return
		}
		writeFleetError(w, err)
		return
	}
	target, err := endpoint.Parse(runtimeState.Endpoint)
	if err != nil {
		writeError(w, http.StatusBadGateway, "proxy_error", "agent endpoint is invalid")
		return
	}
	transport := target.NewTransport()
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Director: func(request *http.Request) {
			request.URL.Scheme = "http"
			request.URL.Host = "juex"
			request.URL.Path = upstreamPath
			request.URL.RawPath = ""
			request.Host = "juex"
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, http.StatusBadGateway, "proxy_error", "agent proxy request failed")
		},
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) serveReadOnlyAgent(
	w http.ResponseWriter,
	r *http.Request,
	selector string,
	upstreamPath string,
) bool {
	if r.Method != http.MethodGet || !isReadOnlyAgentPath(upstreamPath) {
		return false
	}
	stateBackend, ok := s.manager.(readOnlyStateBackend)
	if !ok {
		return false
	}
	state, err := stateBackend.ReadOnlyState(selector)
	if err != nil {
		return false
	}
	request := r.Clone(r.Context())
	request.URL.Path = upstreamPath
	request.URL.RawPath = ""
	web.NewReadOnlyAPIHandler(config.Config{
		WorkDir:       state.Workspace,
		AgentID:       state.ID,
		AgentName:     state.Name,
		AgentStateDir: state.StateDir,
	}).ServeHTTP(w, request)
	return true
}

func isReadOnlyAgentPath(path string) bool {
	if path == "/api/sessions" || path == "/api/media" {
		return true
	}
	const prefix = "/api/sessions/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) == 1 {
		return session.ValidID(parts[0])
	}
	return len(parts) == 2 &&
		session.ValidID(parts[0]) &&
		(parts[1] == "context" || parts[1] == "scratchpad")
}

func parseAgentAPIPath(path string) (string, string, bool) {
	rest := strings.TrimPrefix(path, "/api/agents/")
	if rest == path {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
		return "", "", false
	}
	action := ""
	if len(parts) == 2 {
		if parts[1] == "" {
			return "", "", false
		}
		action = parts[1]
	}
	selector, err := url.PathUnescape(parts[0])
	if err != nil || strings.Contains(selector, "/") {
		return "", "", false
	}
	return selector, action, true
}

func parseAgentProxyPath(path string) (string, string, bool) {
	rest := strings.TrimPrefix(path, "/agents/")
	if rest == path {
		return "", "", false
	}
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return "", "", false
	}
	selector, err := url.PathUnescape(rest[:slash])
	if err != nil || strings.Contains(selector, "/") {
		return "", "", false
	}
	upstreamPath := rest[slash:]
	if upstreamPath != "/api" && !strings.HasPrefix(upstreamPath, "/api/") {
		return "", "", false
	}
	return selector, upstreamPath, true
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("extra JSON value")
	}
	return err
}

func decodeJSONBody(
	w http.ResponseWriter,
	r *http.Request,
	maxBytes int64,
	target any,
	message string,
) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_request", message)
		return false
	}
	if err := requireJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "request body must contain one JSON object")
		return false
	}
	return true
}

func writeFleetError(w http.ResponseWriter, err error) {
	var (
		notFound       *fleet.NotFoundError
		logUnavailable *fleet.LogUnavailableError
		ambiguous      *fleet.AmbiguousSelectorError
		conflict       *fleet.ConflictError
		invalid        *fleet.ConfigValidationError
		validation     *fleet.ValidationError
	)
	switch {
	case errors.As(err, &notFound), errors.As(err, &logUnavailable):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.As(err, &ambiguous), errors.As(err, &conflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
	case errors.As(err, &validation):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "fleet operation failed")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		Error: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: code, Message: message},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func validLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
