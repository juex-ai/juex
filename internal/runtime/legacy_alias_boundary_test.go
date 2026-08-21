package runtime

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyModuleAliasesAreAbsent(t *testing.T) {
	repoRoot := legacyAliasRepositoryRoot(t)
	aliasPath := filepath.Join(repoRoot, "internal", "runtime", "workmem_aliases.go")
	if _, err := os.Stat(aliasPath); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			t.Fatalf("stat legacy workmem aliases: %v", err)
		}
		t.Fatalf("legacy workmem alias file still exists: %s", aliasPath)
	}

	assertTypeNameAbsent(t, filepath.Join(repoRoot, "internal", "runtime", "module"), "PromptSection")
	assertPackageAliasAbsent(t, filepath.Join(repoRoot, "internal", "runtime"), "workmem")
}

func legacyAliasRepositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve legacy alias boundary test working directory: %v", err)
	}
	for {
		info, err := os.Stat(filepath.Join(dir, "go.mod"))
		if err == nil && !info.IsDir() {
			return dir
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stat go.mod from %s: %v", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("find repository root from %s", dir)
		}
		dir = parent
	}
}

func assertTypeNameAbsent(t *testing.T, dir, forbidden string) {
	t.Helper()
	for filename, file := range parseGoFiles(t, dir, true) {
		ast.Inspect(file, func(node ast.Node) bool {
			typeSpec, ok := node.(*ast.TypeSpec)
			if ok && typeSpec.Name.Name == forbidden {
				t.Errorf("legacy type %s remains in %s", forbidden, filename)
			}
			return true
		})
	}
}

func assertPackageAliasAbsent(t *testing.T, dir, forbiddenPackage string) {
	t.Helper()
	for filename, file := range parseGoFiles(t, dir, false) {
		ast.Inspect(file, func(node ast.Node) bool {
			typeSpec, ok := node.(*ast.TypeSpec)
			if !ok || !typeSpec.Assign.IsValid() {
				return true
			}
			selector, ok := typeSpec.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if ok && ident.Name == forbiddenPackage {
				t.Errorf("legacy alias %s remains in %s", typeSpec.Name.Name, filename)
			}
			return true
		})
	}
}

func parseGoFiles(t *testing.T, dir string, includeTests bool) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	files := make(map[string]*ast.File)
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || (!includeTests && strings.HasSuffix(name, "_test.go")) {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = file
	}
	return files
}
