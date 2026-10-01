//go:build linux || darwin

package maintenance

import (
	"errors"
	"golang.org/x/sys/unix"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDrainWaitsForAdmittedWorkAndPreservesCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "admission.lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	g, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	done, err := g.Enter()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if err := os.WriteFile(filepath.Join(dir, "draining"), []byte("maintenance"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Enter(); !errors.Is(err, ErrDraining) {
		t.Fatalf("admitted during drain: %v", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("drain ignored admitted work: %v", err)
	}
	control, err := g.Control()
	if err != nil {
		t.Fatal(err)
	}
	control()
	done()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Control(); !errors.Is(err, ErrDraining) {
		t.Fatalf("control crossed exclusive barrier: %v", err)
	}
}

func TestHTTPDrainAllowsInspectionAndCancelButRejectsInput(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	if err := os.WriteFile(filepath.Join(dir, "admission.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "draining"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	g, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := g.HTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/threads/events", 204}, {"POST", "/api/threads/cancel", 204}, {"POST", "/api/auth/login", 204}, {"POST", "/api/agents/inputs", 503}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
}
