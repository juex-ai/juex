package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadExtensionPolicyInheritsWhenLowerLayersOmitAllow(t *testing.T) {
	userHome := prepareConfigTest(t)
	defaultHome := filepath.Join(userHome, ".juex")
	instanceHome := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)

	writeTextFile(t, filepath.Join(defaultHome, "juex.yaml"), "extensions:\n  allow: [chanwire, taskline]\n")
	writeTextFile(t, filepath.Join(instanceHome, "juex.yaml"), "sandbox:\n  enabled: true\n")
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "skills:\n  include: []\n")

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.ExtensionPolicy()
	if !policy.Configured {
		t.Fatal("extension allowlist should be configured by the default Home")
	}
	if want := []string{"chanwire", "taskline"}; !reflect.DeepEqual(policy.Allow, want) {
		t.Fatalf("allow = %v, want %v", policy.Allow, want)
	}
}

func TestLoadExtensionPolicyReplacesInheritedAllowlistAsAWhole(t *testing.T) {
	userHome := prepareConfigTest(t)
	defaultHome := filepath.Join(userHome, ".juex")
	instanceHome := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)

	writeTextFile(t, filepath.Join(defaultHome, "juex.yaml"), "extensions:\n  allow: [base, shared]\n")
	writeTextFile(t, filepath.Join(instanceHome, "juex.yaml"), "extensions:\n  allow: [fleet, shared]\n")
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "extensions:\n  allow: [agent]\n")

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.ExtensionPolicy()
	if !policy.Configured {
		t.Fatal("extension allowlist should be configured")
	}
	if want := []string{"agent"}; !reflect.DeepEqual(policy.Allow, want) {
		t.Fatalf("allow = %v, want replacement %v", policy.Allow, want)
	}
}

func TestLoadExtensionPolicyExplicitEmptyListDisablesInheritedExtensions(t *testing.T) {
	userHome := prepareConfigTest(t)
	workDir := t.TempDir()
	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "extensions:\n  allow: [base]\n")
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "extensions:\n  allow: []\n")

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.ExtensionPolicy()
	if !policy.Configured || len(policy.Allow) != 0 {
		t.Fatalf("extension allowlist = %+v, want configured empty allowlist", policy)
	}
}

func TestLoadExtensionPolicyDefaultsToUnconfiguredEmptyAllowlist(t *testing.T) {
	prepareConfigTest(t)
	cfg, err := LoadWithOptions(LoadOptions{WorkDir: t.TempDir(), AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.ExtensionPolicy()
	if policy.Configured || len(policy.Allow) != 0 {
		t.Fatalf("extension allowlist = %+v, want unconfigured empty allowlist", policy)
	}
}

func TestExtensionPolicyIgnoresNamesWhenPolicyIsNotConfigured(t *testing.T) {
	policy := (Config{Extensions: ExtensionPolicy{Allow: []string{"demo"}}}).ExtensionPolicy()
	if policy.Configured || len(policy.Allow) != 0 {
		t.Fatalf("extension allowlist = %+v, want unconfigured policy to expose no allowed names", policy)
	}
}

func TestLoadExtensionPolicyNormalizesDuplicatesWithoutReordering(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "extensions:\n  allow: [beta, alpha, beta]\n")

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"beta", "alpha"}; !reflect.DeepEqual(cfg.ExtensionPolicy().Allow, want) {
		t.Fatalf("allow = %v, want %v", cfg.ExtensionPolicy().Allow, want)
	}
}

func TestLoadExtensionPolicyRejectsNonPortableExtensionNames(t *testing.T) {
	for _, name := range []string{"", " ", ".", "..", "../demo", "group/demo", `group\demo`} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			prepareConfigTest(t)
			workDir := t.TempDir()
			yamlName := name
			if strings.TrimSpace(name) == "" {
				yamlName = `"` + name + `"`
			}
			writeTextFile(
				t,
				filepath.Join(workDir, ".juex", "juex.yaml"),
				"extensions:\n  allow:\n    - "+yamlName+"\n",
			)

			_, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
			if err == nil || !strings.Contains(err.Error(), "extensions.allow") {
				t.Fatalf("err = %v, want extensions.allow validation error for %q", err, name)
			}
		})
	}
}

func TestLoadExtensionPolicyRejectsAdHocExplicitOverride(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	explicitPath := filepath.Join(t.TempDir(), "override.yaml")
	writeTextFile(t, explicitPath, "extensions:\n  allow: [demo]\n")

	_, err := LoadWithOptions(LoadOptions{
		WorkDir:    workDir,
		ConfigPath: explicitPath,
		AgentState: AgentStateNone,
	})
	if err == nil ||
		!strings.Contains(err.Error(), "extensions.allow") ||
		!strings.Contains(err.Error(), "default Home, instance Home, or workspace") {
		t.Fatalf("err = %v, want durable Extension-allowlist scope error", err)
	}
}

func TestLoadExtensionPolicyAcceptsExplicitReferenceToWorkspaceConfig(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	workspacePath := filepath.Join(workDir, ".juex", "juex.yaml")
	writeTextFile(t, workspacePath, "extensions:\n  allow: [demo]\n")

	cfg, err := LoadWithOptions(LoadOptions{
		WorkDir:    workDir,
		ConfigPath: workspacePath,
		AgentState: AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"demo"}; !reflect.DeepEqual(cfg.ExtensionPolicy().Allow, want) {
		t.Fatalf("allow = %v, want %v", cfg.ExtensionPolicy().Allow, want)
	}
}

func TestLoadExtensionPolicyAcceptsExplicitReferenceToLoadedHomeConfig(t *testing.T) {
	t.Run("default home", func(t *testing.T) {
		userHome := prepareConfigTest(t)
		defaultPath := filepath.Join(userHome, ".juex", "juex.yaml")
		workDir := t.TempDir()
		writeTextFile(t, defaultPath, `model: local:default
providers:
  - id: local
    protocol: openai/chat
    base_url: http://127.0.0.1:12345
    api_key: test-key
    models:
      - id: default
      - id: workspace
extensions:
  allow: [default]
`)
		writeTextFile(
			t,
			filepath.Join(workDir, ".juex", "juex.yaml"),
			"model: local:workspace\nextensions:\n  allow: [workspace]\n",
		)

		cfg, err := LoadWithOptions(LoadOptions{
			WorkDir:    workDir,
			ConfigPath: defaultPath,
			AgentState: AgentStateNone,
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Model != "default" {
			t.Fatalf("model = %q, want explicit default-Home config to override workspace", cfg.Model)
		}
		if want := []string{"workspace"}; !reflect.DeepEqual(cfg.ExtensionPolicy().Allow, want) {
			t.Fatalf("allow = %v, want %v", cfg.ExtensionPolicy().Allow, want)
		}
	})

	t.Run("instance home", func(t *testing.T) {
		prepareConfigTest(t)
		instanceHome := t.TempDir()
		instancePath := filepath.Join(instanceHome, "juex.yaml")
		t.Setenv("JUEX_HOME", instanceHome)
		writeTextFile(t, instancePath, "extensions:\n  allow: [instance]\n")

		cfg, err := LoadWithOptions(LoadOptions{
			WorkDir:    t.TempDir(),
			ConfigPath: instancePath,
			AgentState: AgentStateNone,
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"instance"}; !reflect.DeepEqual(cfg.ExtensionPolicy().Allow, want) {
			t.Fatalf("allow = %v, want %v", cfg.ExtensionPolicy().Allow, want)
		}
	})
}

func TestResourcePathsExposeReadOnlyDefaultAndEffectiveHomeExtensionRoots(t *testing.T) {
	userHome := prepareConfigTest(t)
	instanceHome := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: t.TempDir(), AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	paths := cfg.ResourcePaths()
	if want := filepath.Join(userHome, ".juex", "extensions"); paths.DefaultHomeExtensionsDir != want {
		t.Fatalf("default Home extensions = %q, want %q", paths.DefaultHomeExtensionsDir, want)
	}
	canonicalInstanceHome, err := filepath.EvalSymlinks(instanceHome)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(canonicalInstanceHome, "extensions"); paths.HomeExtensionsDir != want {
		t.Fatalf("effective Home extensions = %q, want %q", paths.HomeExtensionsDir, want)
	}
}
