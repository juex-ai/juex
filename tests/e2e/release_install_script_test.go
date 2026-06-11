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
	if err := os.WriteFile(checksums, []byte(fmt.Sprintf("%x  %s\n", sum, filepath.Base(archive))), 0o644); err != nil {
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
	return root, filepath.Join(root, "scripts", "install-release.sh")
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
