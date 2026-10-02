//go:build gateway

package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedGatewayAuthenticationLimits(t *testing.T) {
	if os.Getenv("JUEX_GATEWAY_PROBE") == "1" {
		probeGatewayAuthentication(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	arch := run("docker", "info", "--format", "{{.Architecture}}")
	switch arch {
	case "aarch64":
		arch = "arm64"
	case "x86_64":
		arch = "amd64"
	}
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	config, err := os.ReadFile("../../deploy/managed/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	config = []byte(strings.NewReplacer("management:8680", "127.0.0.1:8680", "execution:8683", "127.0.0.1:8683").Replace(string(config)))
	write("nginx.conf", config)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	write("certificate.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	write("key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}))
	build := exec.CommandContext(ctx, "go", "test", "-c", "-tags", "gateway", "-o", filepath.Join(dir, "probe"), ".")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	name := "juex-gateway-test-" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if out, err := exec.CommandContext(cleanup, "docker", "rm", "-f", "-v", name).CombinedOutput(); err != nil {
			t.Errorf("remove gateway: %v\n%s", err, out)
		}
	})
	run("docker", "run", "-d", "--name", name, "-v", dir+":/run/juex/tls:ro", "-v", filepath.Join(dir, "nginx.conf")+":/etc/nginx/nginx.conf:ro", "nginx:1.28-bookworm")
	t.Log(run("docker", "exec", "-e", "JUEX_GATEWAY_PROBE=1", name, "/run/juex/tls/probe", "-test.run=^TestManagedGatewayAuthenticationLimits$", "-test.v"))
}

func probeGatewayAuthentication(t *testing.T) {
	listener, err := net.Listen("tcp", "0.0.0.0:8680")
	if err != nil {
		t.Fatal(err)
	}
	backend := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/auth/recovery" {
			w.Header().Set("Retry-After", "900")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"code":"account_limited"}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":"ok"}`)
	})}
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(func() { _ = backend.Close() })
	client := func(ip string) *http.Client {
		dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
		transport := &http.Transport{DialContext: dialer.DialContext, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // Ephemeral test certificate only.
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 5 * time.Second}
	}
	noisy, other := client("127.0.0.2"), client("127.0.0.3")
	call := func(c *http.Client, method, path, forwarded string) (int, string, string) {
		t.Helper()
		req, err := http.NewRequest(method, "https://127.0.0.1"+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", forwarded)
		req.Header.Set("X-Real-IP", forwarded)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body struct{ Code string }
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal("gateway response must remain JSON", err)
		}
		return res.StatusCode, body.Code, res.Header.Get("Retry-After")
	}
	for i := range 250 {
		status, code, retry := call(noisy, "POST", "/api/auth/login", fmt.Sprintf("192.0.2.%d", i+1))
		if status == 429 {
			if code != "rate_limited" || retry != "1" {
				t.Fatalf("limit response: %s retry=%s", code, retry)
			}
			break
		}
		if status != 200 || i == 249 {
			t.Fatalf("client never limited or unexpected status: %d", status)
		}
	}
	for _, path := range []string{"/api/auth/register-invitation", "/api/auth/set-password"} {
		if status, _, _ := call(noisy, "POST", path, "203.0.113.1"); status != 429 {
			t.Fatalf("%s bypassed shared authentication limit: %d", path, status)
		}
	}
	if status, _, _ := call(other, "POST", "/api/auth/login", "127.0.0.2"); status != 200 {
		t.Fatalf("another client inherited exhausted bucket: %d", status)
	}
	if status, _, _ := call(noisy, "GET", "/api/auth/session", ""); status != 200 {
		t.Fatalf("session read was limited: %d", status)
	}
	if status, _, _ := call(noisy, "POST", "/api/tenants", ""); status != 200 {
		t.Fatalf("non-authentication request was limited: %d", status)
	}
	if status, code, retry := call(other, "POST", "/api/auth/recovery", ""); status != 429 || code != "account_limited" || retry != "900" {
		t.Fatalf("upstream account limit changed: %d %s retry=%s", status, code, retry)
	}
}
