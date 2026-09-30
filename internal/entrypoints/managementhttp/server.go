// Package managementhttp exposes the authenticated platform management API.
package managementhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

type Auth interface {
	Login(context.Context, string, string) (management.Session, error)
	Authenticate(context.Context, string) (management.User, error)
	Logout(context.Context, string) error
	RegisterInvitation(context.Context, string, string) (management.Session, error)
	SetPassword(context.Context, string, string) error
	RequestRecovery(context.Context, string) error
	RequestVerification(context.Context, string) error
	VerifyEmail(context.Context, string) error
}

type Directory interface {
	Tenants(context.Context, string) ([]management.TenantAccess, error)
	Members(context.Context, string, string) ([]management.MemberView, error)
	Invitations(context.Context, string, string) ([]management.InvitationView, error)
	PreviewInvitation(context.Context, string) (management.InvitationPreview, error)
	Invite(context.Context, string, string, string, management.Role, time.Duration) (management.Invitation, string, error)
	AcceptInvitation(context.Context, string, string) (management.Fleet, error)
	ChangeMember(context.Context, string, string, string, management.Role, management.MembershipStatus) (management.Membership, error)
	Fleet(context.Context, string, string, string) (management.Fleet, error)
	FleetOverview(context.Context, string, string, string) (management.FleetOverview, error)
	Models(context.Context, string, string) ([]management.Model, error)
	ConfigureFleet(context.Context, string, string, string, management.FleetSettings) (management.FleetSettings, error)
	CreateAgent(context.Context, string, string, string, management.AgentConfig) (management.Agent, error)
	ConfigureAgent(context.Context, string, string, string, int64, management.AgentConfig) (management.Agent, error)
	SetAgentArchived(context.Context, string, string, string, int64, bool) (management.Agent, error)
	ReadAgent(context.Context, string, string, string) (management.AgentAuthority, error)
}

type Options struct {
	Auth         Auth
	Directory    Directory
	Runtime      Runtime
	Execution    Execution
	PublicURL    string
	InsecureHTTP bool
	MailEnabled  bool
	Static       http.Handler
	Health       func(context.Context) error
}

type Server struct {
	options  Options
	origin   string
	mu       sync.Mutex
	attempts map[string]attempts
}
type attempts struct {
	start time.Time
	count int
}

func New(options Options) (http.Handler, error) {
	origin, err := management.PublicOrigin(options.PublicURL, options.InsecureHTTP)
	if err != nil {
		return nil, err
	}
	if options.Auth == nil || options.Directory == nil {
		return nil, errors.New("management auth and directory are required")
	}
	s := &Server{options: options, origin: origin, attempts: make(map[string]attempts)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]any{"email_enabled": options.MailEnabled}, nil)
	})
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/register-invitation", s.register)
	mux.HandleFunc("POST /api/auth/set-password", s.setPassword)
	mux.HandleFunc("POST /api/auth/recovery", s.recover)
	mux.HandleFunc("POST /api/auth/verify-email", s.verify)
	mux.HandleFunc("POST /api/auth/invitation", s.invitationPreview)
	mux.HandleFunc("POST /api/auth/logout", s.signedIn(s.logout))
	mux.HandleFunc("GET /api/auth/session", s.signedIn(s.session))
	mux.HandleFunc("POST /api/auth/request-verification", s.signedIn(s.requestVerification))
	mux.HandleFunc("GET /api/tenants", s.signedIn(s.tenants))
	mux.HandleFunc("GET /api/tenants/{tenant}/members", s.signedIn(s.members))
	mux.HandleFunc("PATCH /api/tenants/{tenant}/members/{owner}", s.signedIn(s.changeMember))
	mux.HandleFunc("GET /api/tenants/{tenant}/invitations", s.signedIn(s.invitations))
	mux.HandleFunc("POST /api/tenants/{tenant}/invitations", s.signedIn(s.invite))
	mux.HandleFunc("POST /api/auth/accept-invitation", s.signedIn(s.acceptInvitation))
	mux.HandleFunc("GET /api/tenants/{tenant}/fleet", s.signedIn(s.ownFleet))
	mux.HandleFunc("GET /api/tenants/{tenant}/users/{owner}/fleet", s.signedIn(s.fleet))
	mux.HandleFunc("PUT /api/tenants/{tenant}/users/{owner}/fleet/settings", s.signedIn(s.configureFleet))
	mux.HandleFunc("GET /api/tenants/{tenant}/models", s.signedIn(s.models))
	mux.HandleFunc("POST /api/tenants/{tenant}/users/{owner}/agents", s.signedIn(s.createAgent))
	mux.HandleFunc("PUT /api/tenants/{tenant}/agents/{agent}", s.signedIn(s.configureAgent))
	mux.HandleFunc("GET /api/tenants/{tenant}/agents/{agent}", s.signedIn(s.agentDetail))
	mux.HandleFunc("POST /api/tenants/{tenant}/agents/{agent}/archive", s.signedIn(s.archiveAgent))
	if options.Runtime != nil {
		mux.HandleFunc("GET /api/tenants/{tenant}/agents/{agent}/threads", s.signedIn(s.threads))
		mux.HandleFunc("POST /api/tenants/{tenant}/agents/{agent}/inputs", s.signedIn(s.submitInput))
		mux.HandleFunc("GET /api/tenants/{tenant}/agents/{agent}/threads/{thread}/events", s.signedIn(s.threadEvents))
		mux.HandleFunc("POST /api/tenants/{tenant}/agents/{agent}/threads/{thread}/cancel", s.signedIn(s.cancelThread))
		mux.HandleFunc("POST /api/tenants/{tenant}/agents/{agent}/threads/{thread}/workers", s.signedIn(s.createWorker))
	}
	if options.Execution != nil {
		mux.HandleFunc("GET /api/tenants/{tenant}/device-pairings/{pair}", s.signedIn(s.previewPair))
		mux.HandleFunc("POST /api/tenants/{tenant}/device-pairings/{pair}", s.signedIn(s.approvePair))
		mux.HandleFunc("GET /api/tenants/{tenant}/users/{owner}/devices", s.signedIn(s.devices))
		mux.HandleFunc("PUT /api/tenants/{tenant}/devices/{device}/grants", s.signedIn(s.deviceGrants))
		mux.HandleFunc("POST /api/tenants/{tenant}/devices/{device}/revoke", s.signedIn(s.revokeDevice))
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	if options.Static != nil {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			options.Static.ServeHTTP(w, r)
		})
	}
	return s.guard(mux), nil
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin {
				respond(w, nil, management.ErrDenied)
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || media != "application/json" {
					respond(w, nil, management.ErrInvalid)
					return
				}
				// Cookie writes require an origin; native clients use bearer tokens.
				if _, err := r.Cookie("juex_session"); err == nil && r.Header.Get("Origin") == "" && r.Header.Get("Authorization") == "" {
					respond(w, nil, management.ErrDenied)
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			}
			if strings.HasPrefix(r.URL.Path, "/api/auth/") && r.Method == "POST" && !s.allowIP(r.RemoteAddr) {
				respond(w, nil, management.ErrRateLimit)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) allowIP(address string) bool {
	key, _, err := net.SplitHostPort(address)
	if err != nil {
		key = address
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.attempts) >= 4096 {
		for k, v := range s.attempts {
			if now.Sub(v.start) > time.Minute {
				delete(s.attempts, k)
			}
		}
	}
	v, exists := s.attempts[key]
	if !exists && len(s.attempts) >= 4096 {
		return false
	}
	if now.Sub(v.start) > time.Minute {
		v = attempts{start: now}
	}
	v.count++
	s.attempts[key] = v
	return v.count <= 120
}

func (s *Server) signedIn(fn func(http.ResponseWriter, *http.Request, management.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.options.Auth.Authenticate(r.Context(), sessionToken(r))
		if err != nil {
			respond(w, nil, err)
			return
		}
		fn(w, r, user)
	}
}

func sessionToken(r *http.Request) string {
	if value := r.Header.Get("Authorization"); value != "" {
		return strings.TrimPrefix(value, "Bearer ")
	}
	if cookie, err := r.Cookie("juex_session"); err == nil {
		return cookie.Value
	}
	return ""
}

func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, session management.Session, err error) {
	if err != nil {
		respond(w, nil, err)
		return
	}
	if r.Header.Get("X-Juex-Client") == "cli" {
		respond(w, map[string]any{"user": session.User, "token": session.Token, "expires_at": session.ExpiresAt}, nil)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "juex_session", Value: session.Token, Path: "/", HttpOnly: true, Secure: !s.options.InsecureHTTP, SameSite: http.SameSiteLaxMode, Expires: session.ExpiresAt})
	respond(w, session, nil)
}

func decode(r *http.Request, value any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return management.ErrInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return management.ErrInvalid
	}
	return nil
}

func respond(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		status, code, message := http.StatusInternalServerError, "internal_error", "Request could not be completed"
		switch {
		case errors.Is(err, management.ErrInvalid), errors.Is(err, managedruntime.ErrInvalid), errors.Is(err, execprotocol.ErrInvalid):
			status, code, message = 400, "invalid_request", err.Error()
		case errors.Is(err, management.ErrCredentials), errors.Is(err, management.ErrSession):
			status, code, message = 401, "authentication_required", err.Error()
		case errors.Is(err, management.ErrDenied), errors.Is(err, managedruntime.ErrDenied), errors.Is(err, execprotocol.ErrDenied):
			status, code, message = 403, "access_denied", err.Error()
		case errors.Is(err, management.ErrInvitation):
			status, code, message = 400, "invitation_unavailable", err.Error()
		case errors.Is(err, management.ErrConflict), errors.Is(err, management.ErrLoginRequired), errors.Is(err, management.ErrLastAdmin), errors.Is(err, managedruntime.ErrConflict), errors.Is(err, execprotocol.ErrConflict):
			status, code, message = 409, "conflict", err.Error()
		case errors.Is(err, management.ErrRateLimit):
			status, code, message = 429, "rate_limited", err.Error()
			w.Header().Set("Retry-After", "900")
		case errors.Is(err, management.ErrMailUnavailable):
			status, code, message = 503, "email_unavailable", err.Error()
		case errors.Is(err, managedruntime.ErrModelUnavailable):
			status, code, message = 409, "model_unavailable", err.Error()
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "error": message})
		return
	}
	if value == nil {
		value = map[string]bool{"ok": true}
	}
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if s.options.Health != nil {
		if err := s.options.Health(r.Context()); err != nil {
			w.WriteHeader(503)
			return
		}
	}
	respond(w, map[string]string{"service": "management", "status": "ready"}, nil)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	session, err := s.options.Auth.Login(r.Context(), body.Email, body.Password)
	s.writeSession(w, r, session, err)
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	session, err := s.options.Auth.RegisterInvitation(r.Context(), body.Token, body.Password)
	s.writeSession(w, r, session, err)
}
func (s *Server) setPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, nil, s.options.Auth.SetPassword(r.Context(), body.Token, body.Password))
}
func (s *Server) recover(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, nil, s.options.Auth.RequestRecovery(r.Context(), body.Email))
}
func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, nil, s.options.Auth.VerifyEmail(r.Context(), body.Token))
}
func (s *Server) invitationPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Directory.PreviewInvitation(r.Context(), body.Token)
	respond(w, v, err)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ management.User) {
	err := s.options.Auth.Logout(r.Context(), sessionToken(r))
	if err == nil {
		http.SetCookie(w, &http.Cookie{Name: "juex_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: !s.options.InsecureHTTP, SameSite: http.SameSiteLaxMode})
	}
	respond(w, nil, err)
}
func (s *Server) session(w http.ResponseWriter, r *http.Request, user management.User) {
	respond(w, user, nil)
}
func (s *Server) requestVerification(w http.ResponseWriter, r *http.Request, user management.User) {
	respond(w, nil, s.options.Auth.RequestVerification(r.Context(), user.ID))
}
func (s *Server) tenants(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Directory.Tenants(r.Context(), user.ID)
	respond(w, v, err)
}
func (s *Server) members(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Directory.Members(r.Context(), user.ID, r.PathValue("tenant"))
	respond(w, v, err)
}
func (s *Server) invitations(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Directory.Invitations(r.Context(), user.ID, r.PathValue("tenant"))
	respond(w, v, err)
}
func (s *Server) ownFleet(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Directory.FleetOverview(r.Context(), user.ID, r.PathValue("tenant"), user.ID)
	respond(w, v, err)
}
func (s *Server) fleet(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Directory.FleetOverview(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("owner"))
	respond(w, v, err)
}
func (s *Server) changeMember(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct {
		Role   management.Role             `json:"role"`
		Status management.MembershipStatus `json:"status"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Directory.ChangeMember(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("owner"), body.Role, body.Status)
	respond(w, v, err)
}
func (s *Server) invite(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct {
		Email string          `json:"email"`
		Role  management.Role `json:"role"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, token, err := s.options.Directory.Invite(r.Context(), user.ID, r.PathValue("tenant"), body.Email, body.Role, 7*24*time.Hour)
	delivery := "manual"
	if s.options.MailEnabled {
		delivery = "queued"
	}
	respond(w, management.InvitationView{Invitation: v, Link: s.origin + "/join#token=" + token, DeliveryStatus: delivery}, err)
}
func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Directory.AcceptInvitation(r.Context(), user.ID, body.Token)
	respond(w, v, err)
}
