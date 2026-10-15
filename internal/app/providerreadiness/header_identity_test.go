package providerreadiness

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestConnectivityProbeIdentity(t *testing.T) {
	var mu sync.Mutex
	var identities []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		identities = append(identities, r.Header.Get("X-Identity"))
		count := len(identities)
		mu.Unlock()
		if count == 1 {
			w.Header().Set("retry-after-ms", "0")
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	streaming := false
	cfg := config.Config{AgentID: "real-agent", ProviderID: "probe-test", ProviderProtocol: "openai/chat", BaseURL: server.URL, APIKey: "k", Model: "m", ProviderCapabilities: llm.CapabilityOverrides{Streaming: &streaming}, ProviderHeaders: map[string]string{"X-Identity": "${juex_agent_id}/${juex_thread_id}/${juex_generation_id}/${juex_context_scope_id}"}}
	for range 2 {
		if result := CheckConnectivity(context.Background(), cfg, ConnectivityOptions{}); result.Status != StatusOK {
			t.Fatalf("probe = %+v", result)
		}
	}
	cfg.AgentID = ""
	if result := CheckConnectivity(context.Background(), cfg, ConnectivityOptions{}); result.Status != StatusOK {
		t.Fatalf("anonymous probe = %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(identities) != 4 || identities[0] != identities[1] || identities[1] == identities[2] {
		t.Fatalf("identities = %v", identities)
	}
	for i, identity := range identities {
		parts := strings.Split(identity, "/")
		if len(parts) != 4 {
			t.Fatalf("identity = %q", identity)
		}
		if i < 3 && parts[0] != "real-agent" {
			t.Fatalf("agent = %q", parts[0])
		}
		for j, part := range parts {
			if (j > 0 || i == 3) && !strings.HasPrefix(part, "probe-") {
				t.Fatalf("not probe-scoped: %q", identity)
			}
		}
	}
}
