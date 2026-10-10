package migration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
)

func TestFileToolsRetainedConfigurationWire(t *testing.T) {
	source, definition, bindings := agentConfigFixture(false)
	for _, policy := range []int{2} {
		value, err := prepareAgentConfigForPolicy(source, definition, bindings, policy)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(value)
		want := "9205410b12748ebdae09d8cbf385b6b2e16e917bee37bcf21cdd62b1897c49b0"
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
			t.Fatalf("retained policy %d wire changed: %s", policy, got)
		}
		if _, ok := value.Configuration.Modules[agentpolicy.ApplyPatch]; ok {
			t.Fatal("retained policy gained an optional declaration")
		}
	}
}

func TestFileToolsFreshPolicyPreservesOptionalSwitches(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		source, definition, bindings := agentConfigFixture(true)
		source.Modules["apply-patch"], source.Modules["chunked-write"] = enabled, enabled
		value, err := prepareAgentConfig(source, definition, bindings)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []agentpolicy.Capability{agentpolicy.ApplyPatch, agentpolicy.ChunkedWrite} {
			if got, ok := value.Configuration.Modules[key]; !ok || got != enabled {
				t.Fatal("source switch lost", key, value.Configuration)
			}
		}
		if enabled {
			no := false
			bindings.FilesEnabled = &no
			if _, err := prepareAgentConfig(source, definition, bindings); err == nil {
				t.Fatal("unresolved Files dependency accepted")
			}
		}
	}
}
