// Package executionhttp exposes device pairing and device-initiated transport.
package executionhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type Server struct {
	service     *execution.Service
	mu          sync.Mutex
	connections map[string]connectionOwner
	attempts    map[string]attempt
	ctx         context.Context
	handler     http.Handler
	workers     sync.WaitGroup
	closed      bool
}
type attempt struct {
	at    time.Time
	count int
}
type connectionOwner struct {
	epoch  int64
	cancel context.CancelFunc
}

func New(ctx context.Context, service *execution.Service) *Server {
	s := &Server{service: service, ctx: ctx, connections: map[string]connectionOwner{}, attempts: map[string]attempt{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device/pair", s.begin)
	mux.HandleFunc("POST /device/pair/poll", s.poll)
	mux.HandleFunc("POST /device/pair/confirm", s.confirm)
	mux.HandleFunc("GET /device/connect", s.connect)
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Devices use explicit credentials; browser cookies and cross-origin
		// requests are never a device authentication mechanism.
		if r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
			respond(w, nil, execprotocol.ErrDenied)
			return
		}
		if !s.admit(r.RemoteAddr) {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for _, owner := range s.connections {
		owner.cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) admit(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.attempts) > 4096 {
		for key, value := range s.attempts {
			if now.Sub(value.at) > time.Minute {
				delete(s.attempts, key)
			}
		}
	}
	value, exists := s.attempts[host]
	if !exists && len(s.attempts) >= 4096 {
		return false
	}
	if now.Sub(value.at) > time.Minute {
		value = attempt{at: now}
	}
	value.count++
	s.attempts[host] = value
	return value.count <= 240
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		respond(w, nil, execprotocol.ErrInvalid)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		respond(w, nil, execprotocol.ErrInvalid)
		return false
	}
	return true
}

func respond(w http.ResponseWriter, value any, err error) {
	status := http.StatusOK
	if err != nil {
		switch {
		case errors.Is(err, execprotocol.ErrDenied):
			status = http.StatusForbidden
		case errors.Is(err, execprotocol.ErrConflict):
			status = http.StatusConflict
		case errors.Is(err, execprotocol.ErrInvalid), errors.Is(err, execprotocol.ErrVersion):
			status = http.StatusBadRequest
		default:
			status = http.StatusServiceUnavailable
		}
		value = map[string]string{"error": execprotocol.ErrorCode(err)}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) begin(w http.ResponseWriter, r *http.Request) {
	var request execution.PairRequest
	if !decode(w, r, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	value, err := s.service.BeginPair(ctx, request)
	respond(w, value, err)
}
func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if !decode(w, r, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	value, err := s.service.PollPair(ctx, request.ID, request.Secret)
	respond(w, value, err)
}
func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	var request execution.PairConfirmation
	if !decode(w, r, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	value, err := s.service.ConfirmPair(ctx, request)
	respond(w, value, err)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		respond(w, nil, execprotocol.ErrUnavailable)
		return
	}
	s.workers.Add(1)
	s.mu.Unlock()
	defer s.workers.Done()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		respond(w, nil, execprotocol.ErrDenied)
		return
	}
	authCtx, authCancel := context.WithTimeout(ctx, 15*time.Second)
	device, err := s.service.Store.AuthenticateDevice(authCtx, strings.TrimPrefix(header, "Bearer "))
	authCancel()
	if err != nil {
		respond(w, nil, err)
		return
	}
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = connection.CloseNow() }()
	connection.SetReadLimit(3 << 20)
	setup, stopSetup := context.WithTimeout(ctx, 15*time.Second)
	defer stopSetup()
	var hello execprotocol.Envelope
	if err := wsjson.Read(setup, connection, &hello); err != nil {
		return
	}
	if hello.Version != execprotocol.Version {
		_ = wsjson.Write(setup, connection, execprotocol.Envelope{Version: execprotocol.Version, Type: "error", Error: "protocol_version"})
		return
	}
	if hello.Type != "hello" || hello.Environment == nil || hello.Environment.ID != device.ID || hello.Environment.OS != device.OS {
		_ = wsjson.Write(setup, connection, execprotocol.Envelope{Version: execprotocol.Version, Type: "error", Error: "denied"})
		return
	}
	device, err = s.service.Store.Connect(setup, device.ID, hello.Environment.JournalID)
	if err != nil {
		_ = wsjson.Write(setup, connection, execprotocol.Envelope{Version: execprotocol.Version, Type: "error", Error: execprotocol.ErrorCode(err)})
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if previous, ok := s.connections[device.ID]; ok {
		previous.cancel()
	}
	s.connections[device.ID] = connectionOwner{epoch: device.ConnectionEpoch, cancel: cancel}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if current := s.connections[device.ID]; current.epoch == device.ConnectionEpoch {
			delete(s.connections, device.ID)
		}
		s.mu.Unlock()
	}()
	grants, err := s.service.EffectiveGrants(setup, device)
	if err != nil {
		grants = map[string][]execprotocol.Capability{}
	}
	if err := wsjson.Write(setup, connection, execprotocol.Envelope{Version: execprotocol.Version, Type: "welcome", Grants: grants, Revoked: device.Status != "active"}); err != nil {
		return
	}
	// Read continuously even when the durable queue is empty. WebSocket control
	// frames (notably device pings) are serviced by Read, not by the TCP socket.
	type response struct {
		value execprotocol.Envelope
		err   error
	}
	incoming := make(chan response, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			var value execprotocol.Envelope
			err := wsjson.Read(ctx, connection, &value)
			select {
			case incoming <- response{value, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()
	defer func() { cancel(); _ = connection.CloseNow(); <-readerDone }()
	_ = s.service.Connected(ctx, device, func(call context.Context, request execprotocol.Envelope) (execprotocol.Envelope, error) {
		if err := wsjson.Write(call, connection, request); err != nil {
			return execprotocol.Envelope{}, err
		}
		select {
		case reply := <-incoming:
			return reply.value, reply.err
		case <-call.Done():
			return execprotocol.Envelope{}, call.Err()
		}
	})
}
