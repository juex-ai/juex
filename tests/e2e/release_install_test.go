//go:build linux || darwin

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
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

func TestReleaseInstallerInstallsBothClientsAndRejectsCorruption(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	packageName := "juex_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH
	var payload bytes.Buffer
	zipped := gzip.NewWriter(&payload)
	archive := tar.NewWriter(zipped)
	for _, name := range []string{"juex", "juex-executor"} {
		binary := filepath.Join(t.TempDir(), name)
		cmd := exec.Command("go", "build", "-ldflags", "-X github.com/juex-ai/juex/internal/foundation/version.Version=1.2.3", "-o", binary, "./cmd/"+name)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", name, err, out)
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		if err := archive.WriteHeader(&tar.Header{Name: packageName + "/bin/" + name, Mode: 0755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(payload.Bytes()))
	var corrupt atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksums.txt" {
			digest := sum
			if corrupt.Load() {
				digest = strings.Repeat("0", 64)
			}
			_, _ = fmt.Fprintf(w, "%s  %s.tar.gz\n", digest, packageName)
			return
		}
		if r.URL.Path == "/"+packageName+".tar.gz" {
			_, _ = w.Write(payload.Bytes())
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	prefix := t.TempDir()
	invoke := func() ([]byte, error) {
		cmd := exec.Command("uv", "run", "--quiet", "--project", root, "python", filepath.Join(root, "scripts/install.py"), "--prefix", prefix, "--version", "1.2.3", "--release-base-url", server.URL)
		cmd.Dir = root
		return cmd.CombinedOutput()
	}
	if out, err := invoke(); err != nil {
		t.Fatalf("install: %v %s", err, out)
	}
	installed := map[string]string{}
	for _, name := range []string{"juex", "juex-executor"} {
		path := filepath.Join(prefix, "bin", name)
		target, err := os.Readlink(path)
		if err != nil {
			t.Fatal(err)
		}
		installed[name] = target
		out, err := exec.Command(path, "--version").CombinedOutput()
		if err != nil || !strings.Contains(string(out), "1.2.3") {
			t.Fatalf("installed %s: %v %s", name, err, out)
		}
	}
	corrupt.Store(true)
	if out, err := invoke(); err == nil || !strings.Contains(string(out), "checksum mismatch") {
		t.Fatalf("corrupt package accepted: %v %s", err, out)
	}
	for name, want := range installed {
		got, err := os.Readlink(filepath.Join(prefix, "bin", name))
		if err != nil || got != want {
			t.Fatal("failed validation replaced installed release", name, err)
		}
	}
}
