package managementhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/platformrpc"
)

func TestUnavailablePlatformServiceResponse(t *testing.T) {
	response := httptest.NewRecorder()
	respond(response, nil, fmt.Errorf("runtime: %w", platformrpc.ErrUnavailable))
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusServiceUnavailable || body["code"] != "service_unavailable" || body["error"] != platformrpc.ErrUnavailable.Error() {
		t.Fatalf("unavailable platform response = %d %s", response.Code, response.Body.String())
	}
}

func TestUnknownServiceFailureRemainsInternal(t *testing.T) {
	response := httptest.NewRecorder()
	respond(response, nil, errors.New("private database detail"))
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || body["code"] != "internal_error" || body["error"] != "Request could not be completed" {
		t.Fatalf("internal response = %d %s", response.Code, response.Body.String())
	}
}
