package extensionpolicy

import (
	"fmt"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
)

func TestManifestRejectsMergedEnvironmentThatCannotExecute(t *testing.T) {
	defaults := map[string]string{}
	for i := 0; i < 64; i++ {
		defaults[fmt.Sprintf("VAR_%d", i)] = "value"
	}
	manifest := Manifest{ManifestVersion: 2, Name: "example", Version: "1", Environment: defaults, Hooks: []hookpolicy.Declaration{{ID: "sample", Enabled: true, Events: []hookpolicy.Event{hookpolicy.Stop}, Command: []string{"/bin/true"}, Environment: map[string]string{"EXTRA": "value"}}}}
	if manifest.Validate() == nil {
		t.Fatal("accepted 65 merged environment keys")
	}
	delete(defaults, "VAR_63")
	if err := manifest.Validate(); err != nil {
		t.Fatal("valid merged environment", err)
	}
	manifest.Hooks[0].Environment = map[string]string{"JUEX_EXT_DIR": "forged"}
	if manifest.Validate() == nil {
		t.Fatal("accepted reserved environment override")
	}
}
