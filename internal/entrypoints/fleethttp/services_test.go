package fleethttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFleetServicesUnavailableWithoutManager(t *testing.T) {
	handler := newServer(&fakeBackend{}, Options{}).Handler()
	for _, path := range []string{"/api/services", "/api/services/memory"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d, want unavailable", path, response.Code)
		}
	}
}
