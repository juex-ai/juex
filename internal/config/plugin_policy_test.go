package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadPluginPolicyInheritsWhenLowerLayersOmitAllow(t *testing.T) {
	userHome := prepareConfigTest(t)
	defaultHome := filepath.Join(userHome, ".juex")
	instanceHome := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)

	writeTextFile(t, filepath.Join(defaultHome, "juex.yaml"), "plugins:\n  allow: [chanwire, taskline]\n")
	writeTextFile(t, filepath.Join(instanceHome, "juex.yaml"), "sandbox:\n  enabled: true\n")
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "skills:\n  include: []\n")

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.PluginPolicy()
	if !policy.Configured {
		t.Fatal("plugin policy should be configured by the default Home")
	}
	if want := []string{"chanwire", "taskline"}; !reflect.DeepEqual(policy.Allow, want) {
		t.Fatalf("allow = %v, want %v", policy.Allow, want)
	}
}

func TestLoadPluginPolicyReplacesInheritedAllowlistAsAWhole(t *testing.T) {
	userHome := prepareConfigTest(t)
	defaultHome := filepath.Join(userHome, ".juex")
	instanceHome := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)

	writeTextFile(t, filepath.Join(defaultHome, "juex.yaml"), "plugins:\n  allow: [base, shared]\n")
	writeTextFile(t, filepath.Join(instanceHome, "juex.yaml"), "plugins:\n  allow: [fleet, shared]\n")
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "plugins:\n  allow: [agent]\n")

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.PluginPolicy()
	if !policy.Configured {
		t.Fatal("plugin policy should be configured")
	}
	if want := []string{"agent"}; !reflect.DeepEqual(policy.Allow, want) {
		t.Fatalf("allow = %v, want replacement %v", policy.Allow, want)
	}
}

func TestLoadPluginPolicyExplicitEmptyListDisablesInheritedPlugins(t *testing.T) {
	userHome := prepareConfigTest(t)
	workDir := t.TempDir()
	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "plugins:\n  allow: [base]\n")
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "plugins:\n  allow: []\n")

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.PluginPolicy()
	if !policy.Configured || len(policy.Allow) != 0 {
		t.Fatalf("plugin policy = %+v, want configured empty allowlist", policy)
	}
}

func TestLoadPluginPolicyDefaultsToUnconfiguredEmptyAllowlist(t *testing.T) {
	prepareConfigTest(t)
	cfg, err := LoadWithOptions(LoadOptions{WorkDir: t.TempDir(), AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.PluginPolicy()
	if policy.Configured || len(policy.Allow) != 0 {
		t.Fatalf("plugin policy = %+v, want unconfigured empty allowlist", policy)
	}
}

func TestPluginPolicyIgnoresNamesWhenPolicyIsNotConfigured(t *testing.T) {
	policy := (Config{Plugins: PluginPolicy{Allow: []string{"demo"}}}).PluginPolicy()
	if policy.Configured || len(policy.Allow) != 0 {
		t.Fatalf("plugin policy = %+v, want unconfigured policy to expose no allowed names", policy)
	}
}

func TestLoadPluginPolicyNormalizesDuplicatesWithoutReordering(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "plugins:\n  allow: [beta, alpha, beta]\n")

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"beta", "alpha"}; !reflect.DeepEqual(cfg.PluginPolicy().Allow, want) {
		t.Fatalf("allow = %v, want %v", cfg.PluginPolicy().Allow, want)
	}
}

func TestLoadPluginPolicyRejectsNonPortablePluginNames(t *testing.T) {
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
				"plugins:\n  allow:\n    - "+yamlName+"\n",
			)

			_, err := LoadWithOptions(LoadOptions{WorkDir: workDir, AgentState: AgentStateNone})
			if err == nil || !strings.Contains(err.Error(), "plugins.allow") {
				t.Fatalf("err = %v, want plugins.allow validation error for %q", err, name)
			}
		})
	}
}

func TestLoadPluginPolicyRejectsAdHocExplicitOverride(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	explicitPath := filepath.Join(t.TempDir(), "override.yaml")
	writeTextFile(t, explicitPath, "plugins:\n  allow: [demo]\n")

	_, err := LoadWithOptions(LoadOptions{
		WorkDir:    workDir,
		ConfigPath: explicitPath,
		AgentState: AgentStateNone,
	})
	if err == nil ||
		!strings.Contains(err.Error(), "plugins.allow") ||
		!strings.Contains(err.Error(), "default Home, instance Home, or workspace") {
		t.Fatalf("err = %v, want durable plugin-policy scope error", err)
	}
}

func TestLoadPluginPolicyAcceptsExplicitReferenceToWorkspaceConfig(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	workspacePath := filepath.Join(workDir, ".juex", "juex.yaml")
	writeTextFile(t, workspacePath, "plugins:\n  allow: [demo]\n")

	cfg, err := LoadWithOptions(LoadOptions{
		WorkDir:    workDir,
		ConfigPath: workspacePath,
		AgentState: AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"demo"}; !reflect.DeepEqual(cfg.PluginPolicy().Allow, want) {
		t.Fatalf("allow = %v, want %v", cfg.PluginPolicy().Allow, want)
	}
}

func TestLoadPluginPolicyAcceptsExplicitReferenceToLoadedHomeConfig(t *testing.T) {
	t.Run("default home", func(t *testing.T) {
		userHome := prepareConfigTest(t)
		defaultPath := filepath.Join(userHome, ".juex", "juex.yaml")
		writeTextFile(t, defaultPath, "plugins:\n  allow: [default]\n")

		cfg, err := LoadWithOptions(LoadOptions{
			WorkDir:    t.TempDir(),
			ConfigPath: defaultPath,
			AgentState: AgentStateNone,
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"default"}; !reflect.DeepEqual(cfg.PluginPolicy().Allow, want) {
			t.Fatalf("allow = %v, want %v", cfg.PluginPolicy().Allow, want)
		}
	})

	t.Run("instance home", func(t *testing.T) {
		prepareConfigTest(t)
		instanceHome := t.TempDir()
		instancePath := filepath.Join(instanceHome, "juex.yaml")
		t.Setenv("JUEX_HOME", instanceHome)
		writeTextFile(t, instancePath, "plugins:\n  allow: [instance]\n")

		cfg, err := LoadWithOptions(LoadOptions{
			WorkDir:    t.TempDir(),
			ConfigPath: instancePath,
			AgentState: AgentStateNone,
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"instance"}; !reflect.DeepEqual(cfg.PluginPolicy().Allow, want) {
			t.Fatalf("allow = %v, want %v", cfg.PluginPolicy().Allow, want)
		}
	})
}

func TestResourcePathsExposeReadOnlyDefaultAndEffectiveHomePluginRoots(t *testing.T) {
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
