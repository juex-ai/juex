package management

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrivateEnvironmentChangeRejectsAmbiguousValues(t *testing.T) {
	for _, name := range []string{"HOME", "USERPROFILE", "WORKDIR", "JUEX_HOME"} {
		if (ProcessEnvironmentChange{Set: map[string]string{name: "/different-owner"}}).Validate() == nil {
			t.Fatal("managed identity overridden", name)
		}
	}
	for _, raw := range []string{`null`, `{"set":{"KEY":null}}`, `{"set":{"KEY":2}}`, `{"unknown":1}`, `{"version":-1}`, `{"set":{"KEY":"x"},"remove":["KEY"]}`, `{"remove":["KEY","KEY"]}`, `{"set":{"JUEX_TOKEN":"x"}}`, `{"set":{"KEY":"\u0000"}}`} {
		var change ProcessEnvironmentChange
		if err := json.Unmarshal([]byte(raw), &change); err == nil && change.Validate() == nil {
			t.Fatal("accepted invalid private environment", raw)
		}
	}
	for _, value := range []string{"", strings.Repeat("x", 4096)} {
		if (ProcessEnvironmentChange{Set: map[string]string{"KEY": value}}).Validate() != nil {
			t.Fatal("valid explicit value denied")
		}
	}
	if (ProcessEnvironmentChange{Set: map[string]string{"KEY": strings.Repeat("x", 4097)}}).Validate() == nil {
		t.Fatal("oversized value accepted")
	}
}
