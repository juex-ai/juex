//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/postgres"
)

func managementCall[T any](t *testing.T, client *http.Client, method, address, origin string, body any, status int) T {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, address, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: HTTP %d want %d: %s", method, address, response.StatusCode, status, data)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return result
}

func managementClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}
func linkToken(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	v, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	token := v.Get("token")
	if token == "" {
		t.Fatal("link has no token")
	}
	return token
}

func TestManagementAuthenticationHTTP(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	auth, err := postgres.NewAuth(d)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: d, PublicURL: origin, InsecureHTTP: true, Health: pool.Ping, Static: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("Management Web")) })})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	for path, status := range map[string]int{"/login": 200, "/t/example/fleet": 200, "/api/unknown": 404} {
		res, err := http.Get(origin + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != status {
			t.Fatalf("%s: status %d, want %d", path, res.StatusCode, status)
		}
	}
	admin, member := managementClient(t), managementClient(t)
	link, err := auth.BeginBootstrap(ctx, "Default", "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.BeginBootstrap(ctx, "Another", "attacker@example.com"); !errors.Is(err, management.ErrConflict) {
		t.Fatal("bootstrap can be claimed twice", err)
	}
	bootstrap := linkToken(t, link)
	managementCall[any](t, admin, "POST", origin+"/api/auth/set-password", origin, map[string]string{"token": bootstrap, "password": "admin long password"}, 200)
	managementCall[any](t, admin, "POST", origin+"/api/auth/set-password", origin, map[string]string{"token": bootstrap, "password": "replacement password"}, 400)
	managementCall[any](t, admin, "POST", origin+"/api/auth/login", origin, map[string]string{"email": "admin@example.com", "password": "wrong password"}, 401)
	login := managementCall[map[string]any](t, admin, "POST", origin+"/api/auth/login", origin, map[string]string{"email": "admin@example.com", "password": "admin long password"}, 200)
	if _, exists := login["token"]; exists {
		t.Fatal("browser login exposes its session token to JavaScript")
	}
	tenants := managementCall[[]management.TenantAccess](t, admin, "GET", origin+"/api/tenants", origin, nil, 200)
	if len(tenants) != 1 || tenants[0].Role != management.Admin {
		t.Fatal(tenants)
	}
	tenant := tenants[0].ID
	managementCall[any](t, member, "GET", origin+"/api/tenants/"+tenant+"/members", origin, nil, 401)
	managementCall[any](t, admin, "POST", origin+"/api/tenants/"+tenant+"/invitations", "http://attacker.invalid", map[string]string{"email": "outside@example.com", "role": "admin"}, 403)
	managementCall[any](t, admin, "POST", origin+"/api/tenants/"+tenant+"/invitations", "", map[string]string{"email": "outside@example.com", "role": "admin"}, 403)
	invitation := managementCall[management.InvitationView](t, admin, "POST", origin+"/api/tenants/"+tenant+"/invitations", origin, map[string]string{"email": "member@example.com", "role": "member"}, 200)
	listed := managementCall[[]management.InvitationView](t, admin, "GET", origin+"/api/tenants/"+tenant+"/invitations", origin, nil, 200)
	if len(listed) != 1 || linkToken(t, listed[0].Link) != linkToken(t, invitation.Link) {
		t.Fatal("invitation link not recoverable after refresh")
	}
	var cipher []byte
	if err := pool.QueryRow(ctx, `SELECT token_cipher FROM management.invitations WHERE id=$1`, invitation.ID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte(linkToken(t, invitation.Link))) {
		t.Fatal("invitation token stored in plaintext")
	}
	joined := managementCall[management.Session](t, member, "POST", origin+"/api/auth/register-invitation", origin, map[string]string{"token": linkToken(t, invitation.Link), "password": "member long password"}, 200)
	if joined.User.EmailVerified {
		t.Fatal("manual invitation verified mailbox")
	}
	managementCall[any](t, member, "GET", origin+"/api/tenants/"+tenant+"/members", origin, nil, 403)
	fleet := managementCall[management.Fleet](t, member, "GET", origin+"/api/tenants/"+tenant+"/fleet", origin, nil, 200)
	managementCall[any](t, member, "PATCH", origin+"/api/tenants/"+tenant+"/members/"+joined.User.ID, origin, map[string]string{"role": "admin", "status": "active"}, 403)
	managementCall[any](t, admin, "PATCH", origin+"/api/tenants/"+tenant+"/members/"+joined.User.ID, origin, map[string]string{"role": "member", "status": "suspended"}, 200)
	managementCall[any](t, member, "GET", origin+"/api/tenants/"+tenant+"/fleet", origin, nil, 403)
	managementCall[any](t, admin, "PATCH", origin+"/api/tenants/"+tenant+"/members/"+joined.User.ID, origin, map[string]string{"role": "member", "status": "removed"}, 200)
	again := managementCall[management.InvitationView](t, admin, "POST", origin+"/api/tenants/"+tenant+"/invitations", origin, map[string]string{"email": "member@example.com", "role": "member"}, 200)
	managementCall[any](t, managementClient(t), "POST", origin+"/api/auth/register-invitation", origin, map[string]string{"token": linkToken(t, again.Link), "password": "stolen password attempt"}, 409)
	oldID := joined.User.ID
	rejoined := managementCall[management.Fleet](t, member, "POST", origin+"/api/auth/accept-invitation", origin, map[string]string{"token": linkToken(t, again.Link)}, 200)
	if rejoined.ID != fleet.ID || rejoined.UserID != oldID {
		t.Fatal("existing account or Fleet replaced")
	}
	managementCall[any](t, member, "POST", origin+"/api/auth/login", origin, map[string]string{"email": "member@example.com", "password": "member long password"}, 200)
	recovery, err := auth.OperatorRecovery(ctx, "member@example.com")
	if err != nil {
		t.Fatal(err)
	}
	managementCall[any](t, managementClient(t), "POST", origin+"/api/auth/set-password", origin, map[string]string{"token": linkToken(t, recovery), "password": "new member password"}, 200)
	managementCall[any](t, member, "GET", origin+"/api/auth/session", origin, nil, 401)
	managementCall[any](t, member, "POST", origin+"/api/auth/login", origin, map[string]string{"email": "member@example.com", "password": "member long password"}, 401)
	managementCall[any](t, member, "POST", origin+"/api/auth/login", origin, map[string]string{"email": "member@example.com", "password": "new member password"}, 200)
	managementCall[any](t, member, "POST", origin+"/api/auth/logout", origin, map[string]any{}, 200)
	managementCall[any](t, member, "GET", origin+"/api/auth/session", origin, nil, 401)
}

func TestManagementAuthenticationThroughSharedProxy(t *testing.T) {
	_, directory := managementDatabase(t)
	auth, err := postgres.NewAuth(directory)
	if err != nil {
		t.Fatal(err)
	}
	link, err := auth.BeginBootstrap(context.Background(), "Proxy", "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.SetPassword(context.Background(), linkToken(t, link), "admin long password"); err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewUnstartedServer(nil)
	_ = backend.Listener.Close()
	backend.Listener, err = net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: directory, PublicURL: "http://proxy.example", InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	backend.Config.Handler = handler
	backend.Start()
	t.Cleanup(backend.Close)
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(target))
	_ = proxy.Listener.Close()
	proxy.Listener, err = net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy.Start()
	t.Cleanup(proxy.Close)
	noisy, other := managementClient(t), managementClient(t)
	for range 125 {
		managementCall[any](t, noisy, "POST", proxy.URL+"/api/auth/login", "", map[string]string{"unknown": "invalid"}, 400)
	}
	// Distinct clients share the proxy's upstream address. Its traffic must not
	// consume another account's authentication budget inside Management.
	managementCall[any](t, other, "POST", proxy.URL+"/api/auth/login", "", map[string]string{"email": "admin@example.com", "password": "admin long password"}, 200)
	managementCall[any](t, other, "GET", proxy.URL+"/api/auth/session", "", nil, 200)
}

func TestManagementEmailProofAndRecoveryPurposes(t *testing.T) {
	pool, _ := managementDatabase(t)
	ctx := context.Background()
	box, err := secrets.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	d := postgres.NewDirectory(pool, postgres.Config{Secrets: box, PublicURL: "https://juex.test", MailEnabled: true})
	auth, err := postgres.NewAuth(d)
	if err != nil {
		t.Fatal(err)
	}
	link, err := auth.BeginBootstrap(ctx, "Default", "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := linkToken(t, link)
	if err := auth.VerifyEmail(ctx, bootstrap); !errors.Is(err, management.ErrInvitation) {
		t.Fatal("bootstrap is email proof", err)
	}
	if err := auth.SetPassword(ctx, bootstrap, "admin long password"); err != nil {
		t.Fatal(err)
	}
	session, err := auth.Login(ctx, "admin@example.com", "admin long password")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.RequestRecovery(ctx, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM management.mail_outbox`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unverified email permitted recovery", count, err)
	}
	if err := auth.RequestVerification(ctx, session.User.ID); err != nil {
		t.Fatal(err)
	}
	var id string
	var encrypted []byte
	if err := pool.QueryRow(ctx, `SELECT id,body_cipher FROM management.mail_outbox ORDER BY next_attempt_at DESC LIMIT 1`).Scan(&id, &encrypted); err != nil {
		t.Fatal(err)
	}
	body, err := box.Open("mail:"+id, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	verification := linkToken(t, string(body))
	if err := auth.SetPassword(ctx, verification, "forged recovery password"); !errors.Is(err, management.ErrInvitation) {
		t.Fatal("email verification token resets password", err)
	}
	if err := auth.VerifyEmail(ctx, verification); err != nil {
		t.Fatal(err)
	}
	if err := auth.VerifyEmail(ctx, verification); !errors.Is(err, management.ErrInvitation) {
		t.Fatal("verification token replay", err)
	}
	user, err := auth.Authenticate(ctx, session.Token)
	if err != nil || !user.EmailVerified {
		t.Fatal("session did not reflect email verification", user, err)
	}
	if err := auth.RequestRecovery(ctx, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id,body_cipher FROM management.mail_outbox WHERE subject='Reset your JueX password'`).Scan(&id, &encrypted); err != nil {
		t.Fatal(err)
	}
	body, err = box.Open("mail:"+id, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.SetPassword(ctx, linkToken(t, string(body)), "reset admin password"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(ctx, session.Token); !errors.Is(err, management.ErrSession) {
		t.Fatal("recovery retained human session", err)
	}
}

func TestManagementAuthenticationRateLimitAndExpiry(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	auth, err := postgres.NewAuth(d)
	if err != nil {
		t.Fatal(err)
	}
	link, err := auth.BeginBootstrap(ctx, "Default", "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE management.auth_tokens SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := auth.SetPassword(ctx, linkToken(t, link), "admin long password"); !errors.Is(err, management.ErrInvitation) {
		t.Fatal("expired bootstrap accepted", err)
	}
	for i := 0; i < 11; i++ {
		_, err := auth.Login(ctx, "unknown@example.com", "unusable password")
		want := management.ErrCredentials
		if i == 10 {
			want = management.ErrRateLimit
		}
		if !errors.Is(err, want) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := auth.RequestRecovery(ctx, "admin@example.com"); !errors.Is(err, management.ErrMailUnavailable) {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(ctx, strings.Repeat("x", 64)); !errors.Is(err, management.ErrSession) {
		t.Fatal(err)
	}
}
