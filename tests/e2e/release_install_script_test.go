package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReleaseInstallScriptDryRunResolvesAssets(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	root, script := releaseInstallScript(t)

	cases := []struct {
		name        string
		osName      string
		arch        string
		libc        string
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
			name:        "linux arm64 glibc",
			osName:      "linux",
			arch:        "arm64",
			libc:        "glibc",
			wantArchive: "juex_0.0.1_linux_arm64.tar.gz",
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
				"JUEX_INSTALL_LIBC="+tc.libc,
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

func TestMakeRaceProvisionsRipgrep(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is required")
	}
	cmd := exec.Command(makePath, "-n", "race")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make race dry-run failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"scripts/ensure-ripgrep.sh",
		"go test ./... -race -count=1",
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("make race dry-run missing %q:\n%s", want, out)
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
		t.Run(tc.rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(root, tc.rel))
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			for _, forbidden := range tc.forbidden {
				if strings.Contains(text, forbidden) {
					t.Errorf("%s should not document installer dry-run with %q", tc.rel, forbidden)
				}
			}
		})
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
	binary := []byte(`#!/bin/sh
if [ "${1:-} ${2:-}" = "fleet service-installed" ]; then
  echo false
  exit 0
fi
echo juex fixture
`)
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

func TestReleaseInstallScriptFleetServiceLifecycle(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	_, script := releaseInstallScript(t)
	cases := []struct {
		name        string
		installed   string
		optIn       string
		probeFail   string
		installFail string
		statusFail  string
		wantOutput  string
		wantCalls   []string
		forbidCalls []string
	}{
		{
			name:        "missing service prints hint",
			installed:   "false",
			wantOutput:  "set INSTALL_FLEET_SERVICE=1",
			wantCalls:   []string{"fleet service-installed"},
			forbidCalls: []string{"fleet install", "fleet status --format json"},
		},
		{
			name:       "explicit opt-in installs service",
			installed:  "false",
			optIn:      "1",
			wantOutput: "Installed JueX fleet service by explicit request.",
			wantCalls:  []string{"fleet service-installed", "fleet install", "fleet status --format json"},
		},
		{
			name:       "existing service is refreshed",
			installed:  "true",
			wantOutput: "Refreshed existing JueX fleet service.",
			wantCalls:  []string{"fleet service-installed", "fleet install", "fleet status --format json"},
		},
		{
			name:        "service probe failure is a post-install warning",
			probeFail:   "1",
			wantOutput:  "could not check fleet service status",
			wantCalls:   []string{"fleet service-installed"},
			forbidCalls: []string{"fleet install", "fleet status --format json"},
		},
		{
			name:        "existing service refresh failure is a warning",
			installed:   "true",
			installFail: "1",
			wantOutput:  "failed to refresh existing fleet service",
			wantCalls:   []string{"fleet service-installed", "fleet install"},
			forbidCalls: []string{"fleet status --format json"},
		},
		{
			name:        "explicit service install failure is a warning",
			installed:   "false",
			optIn:       "1",
			installFail: "1",
			wantOutput:  "failed to install the requested fleet service",
			wantCalls:   []string{"fleet service-installed", "fleet install"},
			forbidCalls: []string{"fleet status --format json"},
		},
		{
			name:       "version check failure is a warning",
			installed:  "true",
			statusFail: "1",
			wantOutput: "could not check running agent versions",
			wantCalls:  []string{"fleet service-installed", "fleet install", "fleet status --format json"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			releaseDir := filepath.Join(work, "release")
			if err := os.MkdirAll(releaseDir, 0o755); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(releaseDir, "juex_0.0.1_linux_amd64.tar.gz")
			fixture := []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$FLEET_CALL_LOG"
case "${1:-} ${2:-}" in
  "fleet service-installed")
    if [ "${FAKE_FLEET_PROBE_FAIL:-0}" = "1" ]; then
      exit 17
    fi
    printf '%s\n' "$FAKE_FLEET_INSTALLED"
    ;;
  "fleet install")
    if [ "${FAKE_FLEET_INSTALL_FAIL:-0}" = "1" ]; then
      exit 18
    fi
    ;;
  "fleet status")
    if [ "${FAKE_FLEET_STATUS_FAIL:-0}" = "1" ]; then
      exit 19
    fi
    printf '[]\n'
    ;;
  *)
    printf 'juex fixture\n'
    ;;
esac
`)
			writeTarGz(t, archive, "juex_0.0.1_linux_amd64/juex", fixture)
			sum := sha256File(t, archive)
			if err := os.WriteFile(
				filepath.Join(releaseDir, "checksums.txt"),
				[]byte(fmt.Sprintf("%s  %s\n", sum, filepath.Base(archive))),
				0o644,
			); err != nil {
				t.Fatal(err)
			}

			callLog := filepath.Join(work, "fleet-calls.log")
			cmd := exec.Command("bash", script, "--version", "0.0.1", "--bin-dir", filepath.Join(work, "bin"))
			cmd.Dir = work
			cmd.Env = append(os.Environ(),
				"JUEX_INSTALL_OS=linux",
				"JUEX_INSTALL_ARCH=amd64",
				"JUEX_INSTALL_RELEASE_BASE_URL=release",
				"HOME="+filepath.Join(work, "home"),
				"FLEET_CALL_LOG="+callLog,
				"FAKE_FLEET_INSTALLED="+tc.installed,
				"FAKE_FLEET_PROBE_FAIL="+tc.probeFail,
				"FAKE_FLEET_INSTALL_FAIL="+tc.installFail,
				"FAKE_FLEET_STATUS_FAIL="+tc.statusFail,
				"INSTALL_FLEET_SERVICE="+tc.optIn,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), tc.wantOutput) {
				t.Fatalf("output missing %q:\n%s", tc.wantOutput, out)
			}
			calls, err := os.ReadFile(callLog)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.wantCalls {
				if !strings.Contains(string(calls), want+"\n") {
					t.Fatalf("calls missing %q:\n%s", want, calls)
				}
			}
			for _, forbidden := range tc.forbidCalls {
				if strings.Contains(string(calls), forbidden+"\n") {
					t.Fatalf("calls unexpectedly contain %q:\n%s", forbidden, calls)
				}
			}
		})
	}
}

func TestPOSIXInstallersShareFleetRefreshContract(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"scripts/install.sh", "scripts/install-local.sh"} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, want := range []string{
			"refresh_fleet_service()",
			"fleet service-installed",
			`INSTALL_FLEET_SERVICE:-0`,
			"fleet install",
			"fleet status --format json",
			"Refreshed existing JueX fleet service.",
			"set INSTALL_FLEET_SERVICE=1",
			"could not check fleet service status",
			"failed to refresh existing fleet service",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing fleet refresh contract %q", rel, want)
			}
		}
	}
	localBody, err := os.ReadFile(filepath.Join(root, "scripts/install-local.sh"))
	if err != nil {
		t.Fatal(err)
	}
	localText := string(localBody)
	for _, want := range []string{
		`if "$binary" fleet install --restart-agents; then`,
		`if "$binary" fleet install; then`,
	} {
		if !strings.Contains(localText, want) {
			t.Fatalf("scripts/install-local.sh missing install mode %q", want)
		}
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

func TestReleaseInstallScriptDownloadRetriesAndResumesCurl(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	_, script := releaseInstallScript(t)
	payload := bytes.Repeat([]byte("resilient-release-download\n"), 4096)
	split := len(payload) / 2
	var attempts atomic.Int32
	var resumed atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = fmt.Fprintf(
				rw,
				"HTTP/1.1 200 OK\r\nContent-Length: %d\r\nAccept-Ranges: bytes\r\nConnection: close\r\n\r\n",
				len(payload),
			)
			_, _ = rw.Write(payload[:split])
			_ = rw.Flush()
			_ = conn.Close()
			return
		}

		if r.Header.Get("Range") == fmt.Sprintf("bytes=%d-", split) {
			resumed.Store(true)
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set(
				"Content-Range",
				fmt.Sprintf("bytes %d-%d/%d", split, len(payload)-1, len(payload)),
			)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)-split))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[split:])
			return
		}

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	outPath := filepath.Join(t.TempDir(), "release.tar.gz")
	cmd := exec.Command(
		"bash",
		"-c",
		`source "$SCRIPT"; sleep() { :; }; download_file "$URL" "$OUT"`,
	)
	cmd.Env = append(os.Environ(),
		"SCRIPT="+script,
		"URL="+server.URL+"/release.tar.gz",
		"OUT="+outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("download_file failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded payload = %d bytes, want %d", len(got), len(payload))
	}
	if attempts.Load() < 2 {
		t.Fatalf("download attempts = %d, want at least 2", attempts.Load())
	}
	if !resumed.Load() {
		t.Fatal("retry did not resume from the partial output")
	}
}

func TestReleaseInstallScriptDownloadDoesNotRetryHTTPError(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	_, script := releaseInstallScript(t)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	cmd := exec.Command("bash", "-c", `source "$SCRIPT"; download_file "$URL" "$OUT"`)
	cmd.Env = append(os.Environ(),
		"SCRIPT="+script,
		"URL="+server.URL+"/missing.tar.gz",
		"OUT="+filepath.Join(t.TempDir(), "release.tar.gz"),
	)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("download_file unexpectedly accepted a 404\n%s", out)
	}
	if attempts.Load() != 1 {
		t.Fatalf("404 attempts = %d, want 1", attempts.Load())
	}
}

func TestReleaseInstallScriptDownloadRestartsWhenResumeIsUnsupported(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	_, script := releaseInstallScript(t)
	payload := bytes.Repeat([]byte("restart-release-download\n"), 4096)
	split := len(payload) / 2
	var attempts atomic.Int32
	var resumeAttempted atomic.Bool
	var restarted atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch attempts.Add(1) {
		case 1:
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = fmt.Fprintf(
				rw,
				"HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
				len(payload),
			)
			_, _ = rw.Write(payload[:split])
			_ = rw.Flush()
			_ = conn.Close()
		case 2:
			if r.Header.Get("Range") == fmt.Sprintf("bytes=%d-", split) {
				resumeAttempted.Store(true)
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
			_, _ = w.Write(payload)
		default:
			if r.Header.Get("Range") == "" {
				restarted.Store(true)
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
			_, _ = w.Write(payload)
		}
	}))
	defer server.Close()

	outPath := filepath.Join(t.TempDir(), "release.tar.gz")
	cmd := exec.Command("bash", "-c", `source "$SCRIPT"; download_file "$URL" "$OUT"`)
	cmd.Env = append(os.Environ(),
		"SCRIPT="+script,
		"URL="+server.URL+"/release.tar.gz",
		"OUT="+outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("download_file failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded payload = %d bytes, want %d", len(got), len(payload))
	}
	if !resumeAttempted.Load() || !restarted.Load() {
		t.Fatalf(
			"resume attempted = %v, restarted = %v, attempts = %d",
			resumeAttempted.Load(),
			restarted.Load(),
			attempts.Load(),
		)
	}
}

func TestReleaseInstallScriptWgetFallbackUsesBoundedResume(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	_, script := releaseInstallScript(t)
	wgetStub := `#!/bin/sh
if [ "${1:-}" = "--help" ]; then
  printf '%s\n' "$WGET_HELP"
  exit 0
fi
printf '%s\n' "$@" > "$WGET_LOG"
count=0
if [ -f "$WGET_COUNT" ]; then
  count=$(/bin/cat "$WGET_COUNT")
fi
count=$((count + 1))
printf '%s\n' "$count" > "$WGET_COUNT"
out=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -O) out=$2; shift 2 ;;
    *) shift ;;
  esac
done
if [ "$count" -lt 3 ]; then
  printf 'partial' > "$out"
  exit 1
fi
/bin/cp "$WGET_PAYLOAD" "$out"
`

	for _, tc := range []struct {
		name         string
		help         string
		singleTryArg bool
	}{
		{
			name:         "GNU Wget disables built-in retries",
			help:         "-t,  --tries=NUMBER set number of retries",
			singleTryArg: true,
		},
		{
			name: "BusyBox keeps its supported option set",
			help: "Usage: wget [-cqS] [-O FILE] URL",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			stubDir := filepath.Join(work, "bin")
			if err := os.MkdirAll(stubDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stubDir, "wget"), []byte(wgetStub), 0o755); err != nil {
				t.Fatal(err)
			}
			payloadPath := filepath.Join(work, "payload")
			payload := []byte("wget payload")
			if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(work, "wget.log")
			countPath := filepath.Join(work, "wget.count")
			outPath := filepath.Join(work, "release.tar.gz")
			url := "https://example.invalid/release.tar.gz"
			cmd := exec.Command(
				"bash",
				"-c",
				`source "$SCRIPT"; sleep() { :; }; download_file "$URL" "$OUT"`,
			)
			cmd.Env = append(os.Environ(),
				"PATH="+stubDir,
				"SCRIPT="+script,
				"URL="+url,
				"OUT="+outPath,
				"WGET_HELP="+tc.help,
				"WGET_LOG="+logPath,
				"WGET_COUNT="+countPath,
				"WGET_PAYLOAD="+payloadPath,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("wget download_file failed: %v\n%s", err, out)
			}
			got, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("downloaded payload = %q, want %q", got, payload)
			}
			count, err := os.ReadFile(countPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(count)); got != "3" {
				t.Fatalf("wget attempts = %s, want 3", got)
			}
			logged, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			wantArgs := []string{"-q", "-c"}
			if tc.singleTryArg {
				wantArgs = append(wantArgs, "-t", "1")
			}
			wantArgs = append(wantArgs, url, "-O", outPath)
			want := strings.Join(wantArgs, "\n") + "\n"
			if string(logged) != want {
				t.Fatalf("wget args:\n%s\nwant:\n%s", logged, want)
			}
		})
	}
}

func TestReleaseInstallScriptWgetServerErrorsAreNotRetried(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	_, script := releaseInstallScript(t)
	work := t.TempDir()
	stubDir := filepath.Join(work, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	countPath := filepath.Join(work, "wget.count")
	wgetStub := `#!/bin/sh
if [ "${1:-}" = "--help" ]; then
  printf '%s\n' '-t,  --tries=NUMBER set number of retries'
  exit 0
fi
count=0
if [ -f "$WGET_COUNT" ]; then
  count=$(/bin/cat "$WGET_COUNT")
fi
count=$((count + 1))
printf '%s\n' "$count" > "$WGET_COUNT"
exit 8
`
	if err := os.WriteFile(filepath.Join(stubDir, "wget"), []byte(wgetStub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(
		"bash",
		"-c",
		`source "$SCRIPT"; sleep() { :; }; download_file "$URL" "$OUT"`,
	)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir,
		"SCRIPT="+script,
		"URL=https://example.invalid/missing.tar.gz",
		"OUT="+filepath.Join(work, "release.tar.gz"),
		"WGET_COUNT="+countPath,
	)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("download_file unexpectedly accepted wget server error\n%s", out)
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(count)); got != "1" {
		t.Fatalf("wget attempts = %s, want 1", got)
	}
}

func TestReleaseInstallScriptCurlRetriesAreBounded(t *testing.T) {
	skipReleaseInstallScriptTestIfUnsupported(t)
	_, script := releaseInstallScript(t)
	work := t.TempDir()
	stubDir := filepath.Join(work, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	countPath := filepath.Join(work, "curl.count")
	curlStub := `#!/bin/sh
count=0
if [ -f "$CURL_COUNT" ]; then
  count=$(/bin/cat "$CURL_COUNT")
fi
count=$((count + 1))
printf '%s\n' "$count" > "$CURL_COUNT"
exit 7
`
	if err := os.WriteFile(filepath.Join(stubDir, "curl"), []byte(curlStub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(
		"bash",
		"-c",
		`source "$SCRIPT"; sleep() { :; }; download_file "$URL" "$OUT"`,
	)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir,
		"SCRIPT="+script,
		"URL=https://example.invalid/release.tar.gz",
		"OUT="+filepath.Join(work, "release.tar.gz"),
		"CURL_COUNT="+countPath,
	)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("download_file unexpectedly accepted exhausted retries\n%s", out)
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(count)); got != "6" {
		t.Fatalf("curl attempts = %s, want 6", got)
	}
}

func TestPOSIXReleaseDownloadsShareRetryResumePolicy(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"scripts/install.sh", "scripts/prepare-ripgrep.sh"} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, want := range []string{
			"download_with_curl()",
			`curl -fsSL -C - "$url" -o "$out"`,
			`[[ "$status" -eq 22 || "$attempt" -ge 6 ]]`,
			`[[ "$status" -eq 33 ]]`,
			"download_with_wget()",
			`[[ "$wget_help" == *"--tries="* ]]`,
			`wget_args+=(-t 1)`,
			`wget "${wget_args[@]}" "$url" -O "$out"`,
			`[[ "$status" -eq 8 || "$attempt" -ge 6 ]]`,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing download policy %q", rel, want)
			}
		}
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
