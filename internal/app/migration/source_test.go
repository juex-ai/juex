//go:build darwin || linux

package migration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

func guardedBundleFixture(t *testing.T) (*Bundle, *legacy.SourceGuard) {
	t.Helper()
	source, inputs, header := bundleFixture(t)
	home := source.SourceHome
	for _, name := range []string{"fleet.lock", ".locks/fleet/supervisor-role.lock", ".locks/config-imports-cache.lock", ".locks/services/memory.lock", "services/memory/writer.lock", ".locks/fleet/abc234.lock", ".locks/agents/abc234.lock", ".locks/endpoints/abc234.lock", "agents/abc234/.module-resources.lock"} {
		p := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// An empty fixed-format Memory owner is sufficient for capture here.
	for name, data := range map[string]string{
		"services/memory/state/state.json": `{"fleet":"source-fleet","strategy":"basic","fence":0,"clock":0,"access":{},"uses":{},"requests":{},"keys":{},"sources":{},"suppressed":[],"deleted":{}}`,
	} {
		p := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "services/memory/memory"), 0700); err != nil {
		t.Fatal(err)
	}
	g, err := legacy.AcquireSourceGuard(home, []string{"abc234"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	source, err = g.Capture(home)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "bundle")
	digest, err := WriteBundle(dir, source, inputs, header)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadBundle(dir, digest, header.Target)
	if err != nil {
		t.Fatal(err)
	}
	return b, g
}

func TestBundleSourceVerificationNeverRefreshesFrozenInputs(t *testing.T) {
	for _, mutation := range []string{"none", "config", "absent-workspace-config", "identity", "agent-definition", "closed"} {
		t.Run(mutation, func(t *testing.T) {
			b, g := guardedBundleFixture(t)
			original := b.digest
			switch mutation {
			case "config":
				if err := os.WriteFile(filepath.Join(b.source.SourceHome, "juex.yaml"), []byte("models: []\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "absent-workspace-config":
				p := filepath.Join(b.source.Workspaces[0].Path, ".juex")
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(p, "juex.yaml"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "identity":
				if err := os.WriteFile(filepath.Join(b.source.SourceHome, "fleet.json"), []byte(`{"id":"other-fleet"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "agent-definition":
				if err := os.WriteFile(filepath.Join(b.source.SourceHome, "agents/abc234/agent.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "closed":
				if err := g.Close(); err != nil {
					t.Fatal(err)
				}
			}
			err := b.VerifySource(g)
			if mutation == "none" && err != nil {
				t.Fatal(err)
			}
			if mutation != "none" && err == nil {
				t.Fatal("changed source accepted")
			}
			if b.digest != original {
				t.Fatal("verification refreshed the bundle identity")
			}
		})
	}
}
