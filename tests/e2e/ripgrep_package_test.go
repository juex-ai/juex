package e2e

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestRipgrepAssetManifestPinsEveryReleaseTarget(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(root, "release", "ripgrep-assets.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	wantTargets := []string{
		"darwin_amd64",
		"darwin_arm64",
		"linux_amd64",
		"linux_arm64",
		"linux_armv7",
		"windows_amd64",
		"windows_arm64",
	}
	got := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			t.Fatalf("manifest line has %d fields, want 5: %q", len(fields), line)
		}
		target, version, asset, sizeText, digest := fields[0], fields[1], fields[2], fields[3], fields[4]
		if got[target] {
			t.Fatalf("duplicate target %q", target)
		}
		got[target] = true
		if version != "15.1.0" || !strings.Contains(asset, version) {
			t.Fatalf("target %s version/asset = %q/%q", target, version, asset)
		}
		if size, err := strconv.ParseInt(sizeText, 10, 64); err != nil || size <= 0 {
			t.Fatalf("target %s invalid size %q", target, sizeText)
		}
		if len(digest) != sha256.Size*2 {
			t.Fatalf("target %s invalid sha256 %q", target, digest)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, target := range wantTargets {
		if !got[target] {
			t.Errorf("manifest missing %s", target)
		}
	}
	if len(got) != len(wantTargets) {
		t.Fatalf("manifest targets = %v, want exactly %v", got, wantTargets)
	}
}

func TestPrepareRipgrepPackageVerifiesAndBuildsLayout(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	assetName := "ripgrep-15.1.0-aarch64-apple-darwin.tar.gz"
	assetPath := filepath.Join(work, assetName)
	rgBody := []byte("fake pinned rg")
	writeTarGzEntries(t, assetPath, map[string]tarFixture{
		"ripgrep-15.1.0-aarch64-apple-darwin/rg":          {body: rgBody, mode: 0o755},
		"ripgrep-15.1.0-aarch64-apple-darwin/LICENSE-MIT": {body: []byte("MIT fixture"), mode: 0o644},
		"ripgrep-15.1.0-aarch64-apple-darwin/UNLICENSE":   {body: []byte("Unlicense fixture"), mode: 0o644},
	})
	assetInfo, err := os.Stat(assetPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(work, "assets.tsv")
	line := fmt.Sprintf("darwin_arm64\t15.1.0\t%s\t%d\t%s\r\n", assetName, assetInfo.Size(), sha256File(t, assetPath))
	if err := os.WriteFile(manifest, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(work, "package")
	escapedChecksumDir := installEscapedSHA256SumFixture(t, work)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "prepare-ripgrep.sh"),
		"--target", "darwin_arm64",
		"--juex-version", "1.2.3",
		"--output", gitBashPath(t, output),
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"JUEX_RIPGREP_ASSET_MANIFEST="+gitBashPath(t, manifest),
		"JUEX_RIPGREP_BASE_URL=file://"+gitBashPath(t, work),
		"JUEX_RIPGREP_CACHE="+gitBashPath(t, filepath.Join(work, "cache")),
		"PATH="+escapedChecksumDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepare-ripgrep failed: %v\n%s", err, out)
	}
	for _, rel := range []string{
		"juex-path/rg",
		"juex-resources/licenses/ripgrep/LICENSE-MIT",
		"juex-resources/licenses/ripgrep/UNLICENSE",
	} {
		if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("package missing %s: %v", rel, err)
		}
	}
	var packageManifest struct {
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
	data, err := os.ReadFile(filepath.Join(output, "juex-package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &packageManifest); err != nil {
		t.Fatal(err)
	}
	if packageManifest.SchemaVersion != 1 || packageManifest.JuexVersion != "1.2.3" || packageManifest.Platform.OS != "darwin" || packageManifest.Platform.Arch != "arm64" {
		t.Fatalf("package manifest = %+v", packageManifest)
	}
	if packageManifest.Ripgrep.Version != "15.1.0" || packageManifest.Ripgrep.Path != "juex-path/rg" {
		t.Fatalf("ripgrep manifest = %+v", packageManifest.Ripgrep)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(rgBody)); packageManifest.Ripgrep.SHA256 != want {
		t.Fatalf("rg sha256 = %s, want %s", packageManifest.Ripgrep.SHA256, want)
	}
}

func TestReleaseInstallScriptInstallsManagedPackage(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	_, script := releaseInstallScript(t)
	work := t.TempDir()
	releaseDir := filepath.Join(work, "release")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRelease := func(version string) (string, []byte) {
		archive := filepath.Join(releaseDir, "juex_"+version+"_linux_amd64.tar.gz")
		rgBody := []byte("fake packaged rg " + version)
		rgDigest := fmt.Sprintf("%x", sha256.Sum256(rgBody))
		manifest := fmt.Sprintf(`{"schema_version":1,"juex_version":"%s","platform":{"os":"linux","arch":"amd64"},"ripgrep":{"version":"15.1.0","path":"juex-path/rg","sha256":"%s"}}`, version, rgDigest)
		binary := []byte(fmt.Sprintf(`#!/bin/sh
if [ "${1:-} ${2:-}" = "fleet service-installed" ]; then
  echo false
  exit 0
fi
echo juex package fixture %s
`, version))
		root := "juex_" + version + "_linux_amd64"
		writeTarGzEntries(t, archive, map[string]tarFixture{
			root + "/juex-package.json":                           {body: []byte(manifest), mode: 0o644},
			root + "/bin/juex":                                    {body: binary, mode: 0o755},
			root + "/juex-path/rg":                                {body: rgBody, mode: 0o755},
			root + "/juex-resources/licenses/ripgrep/LICENSE-MIT": {body: []byte("MIT"), mode: 0o644},
			root + "/juex-resources/licenses/ripgrep/UNLICENSE":   {body: []byte("Unlicense"), mode: 0o644},
		})
		return archive, rgBody
	}
	archiveV1, rgV1 := writeRelease("0.0.1")
	archiveV2, rgV2 := writeRelease("0.0.2")
	checksums := fmt.Sprintf(
		"%s  %s\n%s  %s\n",
		sha256File(t, archiveV1),
		filepath.Base(archiveV1),
		sha256File(t, archiveV2),
		filepath.Base(archiveV2),
	)
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
	escapedChecksumDir := installEscapedSHA256SumFixture(t, work)
	runInstall := func(version string) {
		t.Helper()
		cmd := exec.Command("bash", script, "--version", version, "--prefix", "relative-prefix")
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"JUEX_INSTALL_OS=linux",
			"JUEX_INSTALL_ARCH=amd64",
			"JUEX_INSTALL_RELEASE_BASE_URL=release",
			"HOME="+filepath.Join(work, "home"),
			"PATH="+escapedChecksumDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("managed install %s failed: %v\n%s", version, err, out)
		}
	}
	runInstall("0.0.1")

	prefix := filepath.Join(work, "relative-prefix")
	current := filepath.Join(prefix, "lib", "juex", "current")
	firstTarget, err := os.Readlink(current)
	if err != nil || !strings.HasPrefix(firstTarget, filepath.Join("releases", "0.0.1-linux-amd64-")) {
		t.Fatalf("current symlink = %q, %v", firstTarget, err)
	}
	installed := filepath.Join(prefix, "bin", "juex")
	if info, err := os.Lstat(installed); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("installed command is not a symlink: %v %v", info, err)
	}
	packageHome, err := filepath.EvalSymlinks(filepath.Join(prefix, "lib", "juex"))
	if err != nil {
		t.Fatal(err)
	}
	firstLauncherTarget, err := os.Readlink(installed)
	firstReleaseRoot := filepath.Join(packageHome, firstTarget)
	if err != nil || firstLauncherTarget != filepath.Join(firstReleaseRoot, "bin", "juex") {
		t.Fatalf("installed command target = %q, %v; want immutable release %q", firstLauncherTarget, err, firstReleaseRoot)
	}
	versionOut, err := exec.Command(installed, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(versionOut), "juex package fixture 0.0.1") {
		t.Fatalf("installed command failed: %v\n%s", err, versionOut)
	}

	runInstall("0.0.1")
	secondTarget, err := os.Readlink(current)
	if err != nil || !strings.HasPrefix(secondTarget, filepath.Join("releases", "0.0.1-linux-amd64-")) {
		t.Fatalf("reinstalled current symlink = %q, %v", secondTarget, err)
	}
	if secondTarget == firstTarget {
		t.Fatalf("same-version reinstall reused mutable release directory %q", secondTarget)
	}
	secondLauncherTarget, err := os.Readlink(installed)
	secondReleaseRoot := filepath.Join(packageHome, secondTarget)
	if err != nil || secondLauncherTarget != filepath.Join(secondReleaseRoot, "bin", "juex") {
		t.Fatalf("reinstalled command target = %q, %v; want immutable release %q", secondLauncherTarget, err, secondReleaseRoot)
	}
	oldPackagedRG := filepath.Join(prefix, "lib", "juex", firstTarget, "juex-path", "rg")
	if got, err := os.ReadFile(oldPackagedRG); err != nil || string(got) != string(rgV1) {
		t.Fatalf("previous release generation was not preserved: %q, %v", got, err)
	}

	runInstall("0.0.2")
	if target, err := os.Readlink(current); err != nil || !strings.HasPrefix(target, filepath.Join("releases", "0.0.2-linux-amd64-")) {
		t.Fatalf("upgraded current symlink = %q, %v", target, err)
	}
	packagedRG := filepath.Join(current, "juex-path", "rg")
	if got, err := os.ReadFile(packagedRG); err != nil || string(got) != string(rgV2) {
		t.Fatalf("upgraded packaged rg = %q, %v", got, err)
	}
	versionOut, err = exec.Command(installed, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(versionOut), "juex package fixture 0.0.2") {
		t.Fatalf("upgraded command failed: %v\n%s", err, versionOut)
	}
}

func TestReleaseInstallScriptRejectsTermuxBeforeDownload(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	root, script := releaseInstallScript(t)
	cmd := exec.Command("bash", script, "--dry-run", "--version", "0.0.1")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"ANDROID_ROOT=/system",
		"PREFIX=/data/data/com.termux/files/usr",
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("Termux dry-run unexpectedly succeeded\n%s", out)
	}
	if !strings.Contains(string(out), "Termux/Android") || !strings.Contains(string(out), "bundled ripgrep") {
		t.Fatalf("Termux error is not actionable:\n%s", out)
	}
}

func TestReleaseInstallScriptRejectsLinuxArm64WithoutGlibcBeforeDownload(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	root, script := releaseInstallScript(t)
	cmd := exec.Command("bash", script, "--dry-run", "--version", "0.0.1")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_OS=linux",
		"JUEX_INSTALL_ARCH=arm64",
		"JUEX_INSTALL_LIBC=musl",
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("Linux arm64 musl dry-run unexpectedly succeeded\n%s", out)
	}
	body := string(out)
	for _, want := range []string{"Linux arm64", "glibc", "ripgrep", "source build"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Linux arm64 compatibility error missing %q:\n%s", want, out)
		}
	}
}

func TestLocalInstallScriptRejectsLinuxArm64WithoutGlibcBeforePackaging(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	stubDir := filepath.Join(work, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goStub := `#!/bin/sh
if [ "${1:-}" = "env" ]; then
  case "${2:-}" in
    GOOS) echo linux; exit 0 ;;
    GOARCH) echo arm64; exit 0 ;;
  esac
fi
exit 97
`
	if err := os.WriteFile(filepath.Join(stubDir, "go"), []byte(goStub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "install-local.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_LIBC=musl",
		"JUEX_RIPGREP_BASE_URL=file://"+filepath.Join(work, "missing"),
		"JUEX_RIPGREP_CACHE_DIR="+filepath.Join(work, "cache"),
		"PREFIX="+filepath.Join(work, "prefix"),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("local Linux arm64 musl install unexpectedly succeeded\n%s", out)
	}
	body := string(out)
	for _, want := range []string{"Linux arm64", "glibc", "ripgrep", "source build"} {
		if !strings.Contains(body, want) {
			t.Fatalf("local Linux arm64 compatibility error missing %q:\n%s", want, out)
		}
	}
}

func TestReleasePackagingIncludesManagedRipgrepPayload(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		".goreleaser.yml": {
			"binary: bin/juex",
			"scripts/prepare-ripgrep.sh",
			"juex-package.json",
			"dst: juex-package.json",
			"juex-resources/licenses/ripgrep",
			"wrap_in_directory: true",
		},
		"scripts/build.sh": {
			"scripts/prepare-ripgrep.sh",
			`bin/juex${ext}`,
		},
		"scripts/install-local.sh": {
			"scripts/prepare-ripgrep.sh",
			"lib/juex",
			"juex-path/rg",
			"Termux/Android",
			"replace_symlink",
			`mktemp -d "${RELEASES_DIR}/.${RELEASE_KEY}.tmp.XXXXXX"`,
			"RELEASE_NAME=",
			"pwd -P",
		},
		"scripts/install.sh": {
			"replace_symlink",
			`mktemp -d "${releases_dir}/.${release_key}.tmp.XXXXXX"`,
			"release_name=",
			"pwd -P",
		},
		"scripts/install.ps1": {
			"Install-ManagedPackage",
			`[Guid]::NewGuid().ToString("N")`,
			"juex-path/rg.exe",
			"current.txt",
			"$releaseName",
		},
	}
	for rel, wants := range checks {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s missing %q", rel, want)
			}
		}
	}
	for rel, forbidden := range map[string]string{
		"scripts/install-local.sh": `rm -rf "$RELEASE_DIR"`,
		"scripts/install.sh":       `rm -rf "$release_dir"`,
		"scripts/install.ps1":      `Remove-Item -LiteralPath $releaseDir`,
	} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), forbidden) {
			t.Errorf("%s still mutates an installed release generation with %q", rel, forbidden)
		}
	}
}

func TestPowerShellReleaseInstallerSwitchesPointerAfterBinaryCopy(t *testing.T) {
	_, script := powerShellInstallScript(t)
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	copyCall := bytes.Index(body, []byte(`Install-Binary -Source (Join-Path $releaseDir "bin/juex.exe") -Target $InstallTarget`))
	pointerWrite := bytes.Index(body, []byte(`Set-Content -LiteralPath (Join-Path $ManagedHome "current.txt") -Value $releaseName -NoNewline`))
	if copyCall < 0 || pointerWrite < 0 {
		t.Fatalf("PowerShell managed install is missing copy or pointer update")
	}
	if pointerWrite < copyCall {
		t.Fatalf("PowerShell managed install switches current.txt before the binary copy can succeed")
	}
	normalizeBinDir := bytes.Index(body, []byte(`[System.IO.Path]::GetFullPath($BinDir)`))
	derivePackageHome := bytes.Index(body, []byte(`$PackageHome = Join-Path (Split-Path -Parent $BinDir) "lib/juex"`))
	if normalizeBinDir < 0 || derivePackageHome < 0 || normalizeBinDir > derivePackageHome {
		t.Fatalf("PowerShell installer must normalize BinDir before deriving package home")
	}
}

func TestPowerShellReleaseInstallerDryRunAcceptsRelativeBinDir(t *testing.T) {
	powerShell, ok := findPowerShell()
	if !ok {
		t.Skip("PowerShell not found; relative BinDir contract is checked statically")
	}
	_, script := powerShellInstallScript(t)
	work := t.TempDir()
	cmd := exec.Command(
		powerShell,
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-File", script,
		"-DryRun",
		"-Version", "0.0.1",
		"-BinDir", "bin",
	)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_OS=windows",
		"JUEX_INSTALL_ARCH=amd64",
		"USERPROFILE="+filepath.Join(work, "home"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("relative BinDir dry run failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		filepath.Join(work, "bin", "juex.exe"),
		filepath.Join(work, "lib", "juex"),
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("relative BinDir dry run missing %q:\n%s", want, out)
		}
	}
}

func TestPowerShellReleaseInstallerInstallsManagedPackage(t *testing.T) {
	powerShell, ok := findPowerShell()
	if !ok {
		t.Skip("PowerShell not found; managed installer contract is checked statically")
	}
	_, script := powerShellInstallScript(t)
	work := t.TempDir()
	releaseDir := filepath.Join(work, "release")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(releaseDir, "juex_0.0.1_windows_amd64.zip")
	rgBody := []byte("fake packaged rg.exe")
	rgDigest := fmt.Sprintf("%x", sha256.Sum256(rgBody))
	manifest := fmt.Sprintf(`{"schema_version":1,"juex_version":"0.0.1","platform":{"os":"windows","arch":"amd64"},"ripgrep":{"version":"15.1.0","path":"juex-path/rg.exe","sha256":"%s"}}`, rgDigest)
	binary := []byte("fake packaged juex.exe")
	packageRoot := "juex_0.0.1_windows_amd64"
	writeZipEntries(t, archive, map[string]tarFixture{
		packageRoot + "/juex-package.json":                           {body: []byte(manifest), mode: 0o644},
		packageRoot + "/bin/juex.exe":                                {body: binary, mode: 0o755},
		packageRoot + "/juex-path/rg.exe":                            {body: rgBody, mode: 0o755},
		packageRoot + "/juex-resources/licenses/ripgrep/LICENSE-MIT": {body: []byte("MIT"), mode: 0o644},
		packageRoot + "/juex-resources/licenses/ripgrep/UNLICENSE":   {body: []byte("Unlicense"), mode: 0o644},
	})
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte(fmt.Sprintf("%s  %s\n", sha256File(t, archive), filepath.Base(archive))), 0o644); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(work, "prefix")
	cmd := exec.Command(powerShell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-Version", "0.0.1", "-Prefix", prefix)
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_OS=windows",
		"JUEX_INSTALL_ARCH=amd64",
		"JUEX_INSTALL_RELEASE_BASE_URL="+releaseDir,
		"USERPROFILE="+filepath.Join(work, "home"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("managed PowerShell install failed: %v\n%s", err, out)
	}
	installed, err := os.ReadFile(filepath.Join(prefix, "bin", "juex.exe"))
	if err != nil || string(installed) != string(binary) {
		t.Fatalf("installed juex.exe = %q, %v", installed, err)
	}
	current, err := os.ReadFile(filepath.Join(prefix, "lib", "juex", "current.txt"))
	if err != nil || !strings.HasPrefix(string(current), "0.0.1-windows-amd64-") {
		t.Fatalf("current.txt = %q, %v", current, err)
	}
	releaseRoot := filepath.Join(prefix, "lib", "juex", "releases", string(current))
	packagedRG, err := os.ReadFile(filepath.Join(releaseRoot, "juex-path", "rg.exe"))
	if err != nil || string(packagedRG) != string(rgBody) {
		t.Fatalf("packaged rg.exe = %q, %v", packagedRG, err)
	}
}

type tarFixture struct {
	body []byte
	mode int64
}

func installEscapedSHA256SumFixture(t *testing.T, work string) string {
	t.Helper()

	tool, mode := "", ""
	if path, err := exec.LookPath("sha256sum"); err == nil {
		tool, mode = path, "sha256sum"
	} else if path, err := exec.LookPath("shasum"); err == nil {
		tool, mode = path, "shasum"
	} else {
		t.Skip("sha256sum or shasum is required")
	}

	binDir := filepath.Join(work, "escaped-checksum-bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
if [ "$JUEX_TEST_SHA256_MODE" = "shasum" ]; then
  raw=$("$JUEX_TEST_SHA256_TOOL" -a 256 "$@")
else
  raw=$("$JUEX_TEST_SHA256_TOOL" "$@")
fi
digest=$(printf '%s\n' "$raw" | awk '{sub(/^\\/, "", $1); print $1}')
printf '\\%s  %s\n' "$digest" "$1"
`
	path := filepath.Join(binDir, "sha256sum")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUEX_TEST_SHA256_TOOL", tool)
	t.Setenv("JUEX_TEST_SHA256_MODE", mode)
	return binDir
}

func gitBashPath(t *testing.T, path string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return path
	}
	out, err := exec.Command("cygpath", "-u", path).CombinedOutput()
	if err != nil {
		t.Fatalf("convert path for Git Bash: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTarGzEntries(t *testing.T, path string, entries map[string]tarFixture) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := entries[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: entry.mode, Size: int64(len(entry.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	for _, closer := range []io.Closer{tw, gz, file} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func writeZipEntries(t *testing.T, path string, entries map[string]tarFixture) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := entries[name]
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(os.FileMode(entry.mode))
		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
