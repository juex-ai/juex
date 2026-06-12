package e2e

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseInstallScriptDryRunResolvesAssets(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	root, script := releaseInstallScript(t)

	cases := []struct {
		name        string
		osName      string
		arch        string
		wantArchive string
	}{
		{
			name:        "mac arm64",
			osName:      "darwin",
			arch:        "arm64",
			wantArchive: "juex_0.0.1_darwin_arm64.tar.gz",
		},
		{
			name:        "linux amd64",
			osName:      "linux",
			arch:        "amd64",
			wantArchive: "juex_0.0.1_linux_amd64.tar.gz",
		},
		{
			name:        "linux armv7",
			osName:      "linux",
			arch:        "armv7",
			wantArchive: "juex_0.0.1_linux_armv7.tar.gz",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", script, "--dry-run", "--version", "0.0.1", "--bin-dir", filepath.Join(t.TempDir(), "bin"))
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"JUEX_INSTALL_OS="+tc.osName,
				"JUEX_INSTALL_ARCH="+tc.arch,
				"HOME="+t.TempDir(),
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("dry-run failed: %v\n%s", err, out)
			}
			body := string(out)
			for _, want := range []string{
				"version: 0.0.1",
				"release tag: v0.0.1",
				"archive: " + tc.wantArchive,
				"install target:",
				"uninstall: rm -f",
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("dry-run output missing %q\n%s", want, body)
				}
			}
		})
	}
}

func TestReleaseInstallScriptDryRunWorksFromStdin(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	root, script := releaseInstallScript(t)
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "-s", "--", "--dry-run", "--version", "0.0.1", "--bin-dir", filepath.Join(t.TempDir(), "bin"))
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(string(body))
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_OS=linux",
		"JUEX_INSTALL_ARCH=amd64",
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stdin dry-run failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "archive: juex_0.0.1_linux_amd64.tar.gz") {
		t.Fatalf("stdin dry-run output missing archive\n%s", out)
	}
}

func TestReleaseInstallScriptsLiveUnderScriptsDirectory(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"scripts/install.sh",
		"scripts/install.ps1",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
	for _, rel := range []string{
		"install.sh",
		"install.ps1",
		"scripts/install-release.sh",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Fatalf("%s should not exist", rel)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", rel, err)
		}
	}
}

func TestMakefileDoesNotExposeReleaseInstaller(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "install-release:") {
			t.Fatalf("Makefile should not define install-release target")
		}
		if strings.HasPrefix(trimmed, ".PHONY:") {
			for _, target := range strings.Fields(strings.TrimPrefix(trimmed, ".PHONY:")) {
				if target == "install-release" {
					t.Fatalf("Makefile should not include install-release in .PHONY")
				}
			}
		}
		if strings.HasPrefix(trimmed, "@echo") && strings.Contains(trimmed, "install-release") {
			t.Fatalf("Makefile help should not expose install-release")
		}
	}
}

func TestInstallerDryRunIsInternalOnly(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		rel       string
		forbidden []string
	}{
		{
			rel: "README.md",
			forbidden: []string{
				"Preview the install without writing files",
				"bash -s -- --dry-run",
				".\\install.ps1 -DryRun",
			},
		},
		{
			rel: "ARCHITECTURE.md",
			forbidden: []string{
				"supports `--dry-run`",
			},
		},
		{
			rel: "scripts/install.sh",
			forbidden: []string{
				"#   scripts/install.sh --dry-run",
				"[--dry-run]",
				"--dry-run          Print the install plan",
			},
		},
	}
	for _, tc := range cases {
		body, err := os.ReadFile(filepath.Join(root, tc.rel))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, forbidden := range tc.forbidden {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s should not document installer dry-run with %q", tc.rel, forbidden)
			}
		}
	}
}

func TestPowerShellInstallerHasDryRunContract(t *testing.T) {
	root, script := powerShellInstallScript(t)
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"param(",
		"[switch]$DryRun",
		"juex.exe",
		"checksums.txt",
		"Get-FileHash",
		"Expand-Archive",
		"Remove-Item -Force",
		"try {",
		"if ($tmp) {",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("install.ps1 missing %q", want)
		}
	}

	powerShell, ok := findPowerShell()
	if !ok {
		t.Skip("PowerShell not found; static install.ps1 contract was checked")
	}
	cmd := exec.Command(powerShell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-DryRun", "-Version", "0.0.1", "-BinDir", filepath.Join(t.TempDir(), "bin"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_OS=windows",
		"JUEX_INSTALL_ARCH=amd64",
		"USERPROFILE="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.ps1 dry-run failed: %v\n%s", err, out)
	}
	output := string(out)
	for _, want := range []string{
		"version: 0.0.1",
		"release tag: v0.0.1",
		"archive: juex_0.0.1_windows_amd64.zip",
		"install target:",
		"juex.exe",
		"uninstall: Remove-Item -Force",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("install.ps1 dry-run output missing %q\n%s", want, output)
		}
	}
}

func TestCIWorkflowExercisesReleaseInstaller(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"Test release installer",
		"scripts/install.sh",
		"scripts/install.ps1",
		"$HOME/.local/bin",
		"GITHUB_PATH",
		"juex version",
		"juex.exe version",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("ci.yml missing %q", want)
		}
	}
}

func TestReleaseInstallScriptDryRunUsesCLIConfigVersion(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	root, script := releaseInstallScript(t)

	cmd := exec.Command("bash", script, "--dry-run", "--bin-dir", filepath.Join(t.TempDir(), "bin"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_OS=linux",
		"JUEX_INSTALL_ARCH=amd64",
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, out)
	}
	body := string(out)
	for _, want := range []string{
		"version: 0.0.1",
		"archive: juex_0.0.1_linux_amd64.tar.gz",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dry-run output missing %q\n%s", want, body)
		}
	}
}

func TestReleaseInstallScriptDryRunStripsCRLFCLIConfigVersion(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	root, script := releaseInstallScript(t)
	config := filepath.Join(t.TempDir(), "CLI_CONFIG")
	if err := os.WriteFile(config, []byte("VERSION=0.3.0\r\nNAME=juex\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", script, "--dry-run", "--bin-dir", filepath.Join(t.TempDir(), "bin"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_CLI_CONFIG="+config,
		"JUEX_INSTALL_OS=linux",
		"JUEX_INSTALL_ARCH=amd64",
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, out)
	}
	body := string(out)
	for _, want := range []string{
		"version: 0.3.0\n",
		"release tag: v0.3.0\n",
		"archive: juex_0.3.0_linux_amd64.tar.gz",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dry-run output missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "\r") {
		t.Fatalf("dry-run output contains carriage return\n%q", body)
	}
}

func TestReleaseInstallScriptDryRunResolvesLatestVersion(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	root, script := releaseInstallScript(t)

	cmd := exec.Command("bash", script, "--dry-run", "--version", "latest", "--bin-dir", filepath.Join(t.TempDir(), "bin"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_LATEST_VERSION=v0.2.0",
		"JUEX_INSTALL_OS=linux",
		"JUEX_INSTALL_ARCH=amd64",
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, out)
	}
	body := string(out)
	for _, want := range []string{
		"version: 0.2.0",
		"release tag: v0.2.0",
		"archive: juex_0.2.0_linux_amd64.tar.gz",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dry-run output missing %q\n%s", want, body)
		}
	}
}

func TestReleaseInstallScriptInstallsFromReleaseDirectory(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	_, script := releaseInstallScript(t)
	work := t.TempDir()
	releaseDir := filepath.Join(work, "release")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(releaseDir, "juex_0.0.1_linux_amd64.tar.gz")
	binary := []byte("#!/bin/sh\necho juex fixture\n")
	writeTarGz(t, archive, "juex_0.0.1_linux_amd64/juex", binary)
	sum := sha256File(t, archive)
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte(fmt.Sprintf("%s  %s\n", sum, filepath.Base(archive))), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(t.TempDir(), "bin")
	cmd := exec.Command("bash", script, "--version", "0.0.1", "--bin-dir", binDir)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"JUEX_INSTALL_OS=linux",
		"JUEX_INSTALL_ARCH=amd64",
		"JUEX_INSTALL_RELEASE_BASE_URL=release",
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Installed juex to ") {
		t.Fatalf("install output missing success line\n%s", out)
	}
	installed := filepath.Join(binDir, "juex")
	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binary) {
		t.Fatalf("installed binary = %q, want %q", got, binary)
	}
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("installed binary mode = %v, want executable bit", info.Mode())
	}
}

func TestReleaseInstallScriptVerifyChecksum(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	_, script := releaseInstallScript(t)
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "juex_0.0.1_linux_amd64.tar.gz")
	body := []byte("release archive bytes")
	if err := os.WriteFile(archive, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	checksums := filepath.Join(tmp, "checksums.txt")
	if err := os.WriteFile(checksums, []byte(fmt.Sprintf("%x  %s\r\n", sum, filepath.Base(archive))), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "-c", `source "$SCRIPT"; verify_checksum "$ARCHIVE" "$CHECKSUMS"`)
	cmd.Env = append(os.Environ(),
		"SCRIPT="+script,
		"ARCHIVE="+archive,
		"CHECKSUMS="+checksums,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("verify_checksum failed: %v\n%s", err, out)
	}

	badChecksums := filepath.Join(tmp, "bad-checksums.txt")
	if err := os.WriteFile(badChecksums, []byte(strings.Repeat("0", 64)+"  "+filepath.Base(archive)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", "-c", `source "$SCRIPT"; verify_checksum "$ARCHIVE" "$CHECKSUMS"`)
	cmd.Env = append(os.Environ(),
		"SCRIPT="+script,
		"ARCHIVE="+archive,
		"CHECKSUMS="+badChecksums,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("verify_checksum succeeded with a bad checksum\n%s", out)
	}
	if !strings.Contains(string(out), "checksum mismatch") {
		t.Fatalf("verify_checksum mismatch output = %q", out)
	}
}

func writeTarGz(t *testing.T, path, name string, body []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func releaseInstallScript(t *testing.T) (string, string) {
	t.Helper()
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "scripts", "install.sh")
}

func powerShellInstallScript(t *testing.T) (string, string) {
	t.Helper()
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "scripts", "install.ps1")
}

func findPowerShell() (string, bool) {
	for _, name := range []string{"pwsh", "powershell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, true
		}
	}
	return "", false
}

func skipReleaseInstallScriptTestIfUnsupported(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("release install script tests require bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found; skipping release install script tests")
	}
}
