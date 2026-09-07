package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/juex-ai/juex"

// Every production package is classified by its owning group, including files
// for other operating systems. New top-level groups require an explicit rule.
var dependencies = map[string]map[string]bool{
	"foundation":  {"foundation": true},
	"framework":   {"foundation": true, "framework": true},
	"features":    {"foundation": true, "framework": true, "features": true},
	"providers":   {"foundation": true, "providers": true},
	"fleet":       {"foundation": true, "framework": true, "fleet": true},
	"app":         {"foundation": true, "framework": true, "features": true, "providers": true, "fleet": true, "app": true},
	"entrypoints": {"foundation": true, "framework": true, "features": true, "providers": true, "fleet": true, "app": true, "entrypoints": true},
	"cmd":         {"foundation": true, "entrypoints": true},
}

func packageGroup(path string) string {
	if strings.HasPrefix(path, "cmd/") {
		return "cmd"
	}
	if !strings.HasPrefix(path, "internal/") {
		return ""
	}
	group, _, _ := strings.Cut(strings.TrimPrefix(path, "internal/"), "/")
	if group == "cmd" || dependencies[group] == nil {
		return ""
	}
	return group
}

func TestProductionPackageOwnershipAndDependencies(t *testing.T) {
	root := repositoryRoot(t)
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			owner := packageGroup(filepath.ToSlash(filepath.Dir(relative)))
			if owner == "" {
				t.Errorf("production package has no owner: %s", relative)
				return nil
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range parsed.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if owner == "foundation" && (strings.HasPrefix(imported, "github.com/openai/") || strings.HasPrefix(imported, "github.com/anthropics/")) {
					t.Errorf("Foundation imports Provider SDK: %s -> %s", relative, imported)
				}
				if !strings.HasPrefix(imported, modulePath+"/") {
					continue
				}
				target := packageGroup(strings.TrimPrefix(imported, modulePath+"/"))
				if target == "" {
					t.Errorf("production import has no owner: %s -> %s", relative, imported)
					continue
				}
				if !dependencies[owner][target] {
					t.Errorf("%s cannot depend on %s: %s -> %s", owner, target, relative, imported)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}
}

func TestPackageOwnershipClassifiesNestedAndUnknownRoots(t *testing.T) {
	for path, want := range map[string]string{
		"internal/foundation/events":                    "foundation",
		"internal/framework/agent":                      "framework",
		"internal/features/skills/internal/frontmatter": "features",
		"internal/providers/internal/protocol":          "providers",
		"internal/fleet/service":                        "fleet",
		"internal/app/config":                           "app",
		"internal/entrypoints/agenthttp":                "entrypoints",
		"cmd/juex":                                      "cmd",
		"internal/unclassified":                         "",
		"internal/cmd":                                  "",
		"tests/e2e":                                     "",
	} {
		if got := packageGroup(path); got != want {
			t.Errorf("packageGroup(%q)=%q, want %q", path, got, want)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve boundary test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
