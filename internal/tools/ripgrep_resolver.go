package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/juex-ai/juex/internal/version"
)

type RipgrepSource string

const (
	RipgrepSourceOverride RipgrepSource = "override"
	RipgrepSourcePackage  RipgrepSource = "package"
	RipgrepSourceSystem   RipgrepSource = "system"
)

type ResolvedRipgrep struct {
	Path    string
	Version string
	Source  RipgrepSource
}

type ripgrepResolveOptions struct {
	ExplicitPath   string
	ExecutablePath string
	RuntimeOS      string
	RuntimeArch    string
	JuexVersion    string
	Getenv         func(string) string
	LookPath       func(string) (string, error)
}

type juexPackageManifest struct {
	SchemaVersion int    `json:"schema_version"`
	JuexVersion   string `json:"juex_version"`
	Platform      struct {
		OS   string `json:"os"`
		Arch string `json:"arch"`
	} `json:"platform"`
	Ripgrep struct {
		Version string `json:"version"`
		Path    string `json:"path"`
		SHA256  string `json:"sha256"`
	} `json:"ripgrep"`
}

func ResolveRipgrep() (ResolvedRipgrep, error) {
	executable, _ := os.Executable()
	return resolveRipgrep(ripgrepResolveOptions{
		ExecutablePath: executable,
		RuntimeOS:      runtime.GOOS,
		RuntimeArch:    runtime.GOARCH,
		JuexVersion:    version.Version,
		Getenv:         os.Getenv,
		LookPath:       exec.LookPath,
	})
}

func resolveRipgrep(opts ripgrepResolveOptions) (ResolvedRipgrep, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	explicit := strings.TrimSpace(opts.ExplicitPath)
	if explicit == "" {
		explicit = strings.TrimSpace(getenv("JUEX_RG"))
	}
	if explicit != "" {
		path, err := validateExecutableFile(explicit)
		if err != nil {
			return ResolvedRipgrep{}, fmt.Errorf("grep: JUEX_RG: %w", err)
		}
		return ResolvedRipgrep{Path: path, Source: RipgrepSourceOverride}, nil
	}

	executable := strings.TrimSpace(opts.ExecutablePath)
	if executable != "" {
		if abs, err := filepath.Abs(executable); err == nil {
			executable = abs
		}
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		if root := findPackageRoot(executable); root != "" {
			return resolvePackageRipgrep(root, opts)
		}
		if root, managed, err := managedPackageRoot(executable, opts); err != nil {
			return ResolvedRipgrep{}, err
		} else if managed {
			return resolvePackageRipgrep(root, opts)
		}
	}

	path, err := lookPath("rg")
	if err != nil {
		return ResolvedRipgrep{}, fmt.Errorf("grep: ripgrep is unavailable; install JueX from a release package, set JUEX_RG, or install rg for source development: %w", err)
	}
	path, err = validateExecutableFile(path)
	if err != nil {
		return ResolvedRipgrep{}, fmt.Errorf("grep: system ripgrep: %w", err)
	}
	return ResolvedRipgrep{Path: path, Source: RipgrepSourceSystem}, nil
}

func findPackageRoot(executable string) string {
	dir := filepath.Dir(executable)
	for i := 0; i < 4; i++ {
		if _, err := os.Stat(filepath.Join(dir, "juex-package.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func managedPackageRoot(executable string, opts ripgrepResolveOptions) (string, bool, error) {
	// POSIX managed commands are symlinks into the versioned package, which
	// findPackageRoot identifies above. Looking sideways from an unpackaged
	// prefix/bin binary would mistake a stale lib/juex directory for proof
	// that the binary itself belongs to that package.
	if opts.RuntimeOS != "windows" {
		return "", false, nil
	}

	binDir := filepath.Dir(executable)
	prefix := filepath.Dir(binDir)
	managedHome := filepath.Join(prefix, "lib", "juex")
	info, err := os.Stat(managedHome)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("grep: inspect managed package home: %w", err)
	}
	if !info.IsDir() {
		return "", true, fmt.Errorf("grep: managed package home %s is not a directory", managedHome)
	}
	key := packageReleaseKey(opts.JuexVersion, opts.RuntimeOS, opts.RuntimeArch)
	current, err := os.ReadFile(filepath.Join(managedHome, "current.txt"))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("grep: read managed package pointer: %w", err)
	}
	releaseName := strings.TrimSpace(string(current))
	if releaseName != key && !strings.HasPrefix(releaseName, key+"-") {
		return "", false, nil
	}
	if releaseName == "" || strings.ContainsAny(releaseName, `/\`) {
		return "", true, fmt.Errorf("grep: managed package pointer %q is not a release name", releaseName)
	}
	root := filepath.Join(managedHome, "releases", releaseName)
	if _, err := os.Stat(filepath.Join(root, "juex-package.json")); err != nil {
		return "", true, fmt.Errorf("grep: managed ripgrep package %s is missing: %w", root, err)
	}
	return root, true, nil
}

func packageReleaseKey(juexVersion, runtimeOS, runtimeArch string) string {
	cleanVersion := strings.TrimSpace(strings.TrimPrefix(juexVersion, "v"))
	cleanVersion = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(cleanVersion)
	return cleanVersion + "-" + runtimeOS + "-" + runtimeArch
}

func resolvePackageRipgrep(root string, opts ripgrepResolveOptions) (ResolvedRipgrep, error) {
	manifestPath := filepath.Join(root, "juex-package.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return ResolvedRipgrep{}, fmt.Errorf("grep: read managed ripgrep manifest: %w", err)
	}
	var manifest juexPackageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ResolvedRipgrep{}, fmt.Errorf("grep: parse managed ripgrep manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return ResolvedRipgrep{}, fmt.Errorf("grep: managed ripgrep manifest schema_version=%d, want 1", manifest.SchemaVersion)
	}
	if strings.TrimPrefix(manifest.JuexVersion, "v") != strings.TrimPrefix(opts.JuexVersion, "v") {
		return ResolvedRipgrep{}, fmt.Errorf("grep: managed ripgrep package version %q does not match JueX %q", manifest.JuexVersion, opts.JuexVersion)
	}
	if manifest.Platform.OS != opts.RuntimeOS || manifest.Platform.Arch != opts.RuntimeArch {
		return ResolvedRipgrep{}, fmt.Errorf("grep: managed ripgrep platform %s/%s does not match runtime %s/%s", manifest.Platform.OS, manifest.Platform.Arch, opts.RuntimeOS, opts.RuntimeArch)
	}
	wantRelative := "juex-path/rg"
	if opts.RuntimeOS == "windows" {
		wantRelative += ".exe"
	}
	if filepath.ToSlash(manifest.Ripgrep.Path) != wantRelative {
		return ResolvedRipgrep{}, fmt.Errorf("grep: managed ripgrep manifest path %q, want %q", manifest.Ripgrep.Path, wantRelative)
	}
	path, err := validateExecutableFile(filepath.Join(root, filepath.FromSlash(wantRelative)))
	if err != nil {
		return ResolvedRipgrep{}, fmt.Errorf("grep: managed ripgrep: %w", err)
	}
	if len(manifest.Ripgrep.SHA256) != sha256.Size*2 {
		return ResolvedRipgrep{}, fmt.Errorf("grep: managed ripgrep manifest has invalid sha256")
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return ResolvedRipgrep{}, fmt.Errorf("grep: hash managed ripgrep: %w", err)
	}
	if !strings.EqualFold(actual, manifest.Ripgrep.SHA256) {
		return ResolvedRipgrep{}, fmt.Errorf("grep: managed ripgrep sha256 mismatch: got %s", actual)
	}
	return ResolvedRipgrep{Path: path, Version: manifest.Ripgrep.Version, Source: RipgrepSourcePackage}, nil
}

func validateExecutableFile(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable", path)
	}
	return path, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
