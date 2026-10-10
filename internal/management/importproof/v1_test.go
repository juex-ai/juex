package importproof

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/juex-ai/juex/internal/management"
)

// Fixtures and hashes were produced by the canonicalizers at 288717bc, before
// the declaration schema changed. They must not be regenerated from this code.
func TestV1GoldenProofs(t *testing.T) {
	for name, want := range map[string]string{
		"nil":   "6e28e4d6e0e7c96186a602b52af45039398938ddf852f45f876b1fbff2a76933",
		"empty": "73197f5d61368ee7b392be189f929a64813c75124e0b6710e2c0eb6c14687b18",
		"full":  "a93421e23f39f4e0ff624c2df2e404fdb9a13696509717ef3eebdd14e3296c81",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/agent-" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var proof AgentsV1
			if err := json.Unmarshal(data, &proof); err != nil {
				t.Fatal(err)
			}
			_, got, err := proof.Prepare()
			if err != nil || got != want {
				t.Fatal("stored Agent proof changed", got, err)
			}
		})
	}
	data, err := os.ReadFile("testdata/models.json")
	if err != nil {
		t.Fatal(err)
	}
	var proof ModelsV1
	if err := json.Unmarshal(data, &proof); err != nil {
		t.Fatal(err)
	}
	_, hash, err := proof.Prepare()
	if err != nil || hash != "90087ef237621ea4d2cf2f6afb89920006c65875c5ac48d5de921b5cb18a5c54" {
		t.Fatal("stored model proof changed", hash, err)
	}
	slices.Reverse(proof.Models)
	for i := range proof.Models {
		proof.Models[i].Configuration.Options.Authentication = ""
		if len(proof.Models[i].Fallbacks) == 0 {
			proof.Models[i].Fallbacks = nil
		}
	}
	_, again, err := proof.Prepare()
	if err != nil || again != hash {
		t.Fatal("normalization changed", err)
	}
}

func TestFreezeModelRejectsLossyUTF8BeforeJSON(t *testing.T) {
	for _, mutate := range []func(*management.ModelConfiguration){
		func(c *management.ModelConfiguration) { c.APIKey = "key\xff" },
		func(c *management.ModelConfiguration) { c.Name = "model\xff" },
		func(c *management.ModelConfiguration) { c.Options.Headers = map[string]string{"key\xff": "value"} },
		func(c *management.ModelConfiguration) { c.Options.Query = map[string]string{"key": "value\xff"} },
		func(c *management.ModelConfiguration) { c.Options.Compat.ReasoningReplayFields = []string{"value\xff"} },
	} {
		config := management.ModelConfiguration{}
		mutate(&config)
		if _, err := FreezeModel(config); !errors.Is(err, management.ErrInvalid) {
			t.Fatal("accepted lossy UTF-8", err)
		}
	}
	config := management.ModelConfiguration{APIKey: "literal replacement \ufffd"}
	got, err := FreezeModel(config)
	if err != nil || got.APIKey != config.APIKey {
		t.Fatal("valid Unicode changed", err)
	}
}
