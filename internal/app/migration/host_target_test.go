package migration

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/execution/host"
)

func hostTargetFixture(t *testing.T) (string, BundleTarget) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"secrets", "socket", "blobs", "maintenance", "workspaces", "control"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"workspaces", "control"} {
		if err := os.WriteFile(filepath.Join(dir, name, "owner.json"), []byte(`{"deployment_id":"11111111-1111-4111-8111-111111111111"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	id := "11111111-1111-4111-8111-111111111111"
	metadata := map[string]any{"format": 2, "backend": "host", "root": dir, "workspace": filepath.Join(dir, "workspaces"), "identity": id, "public_url": "https://machine.example:8443", "other_operator_setting": true}
	hostConfig := managed.HostConfiguration{Backend: host.Config{Root: filepath.Join(dir, "workspaces"), ControlRoot: filepath.Join(dir, "control"), Identity: id, Server: "https://machine.example:8443"}, KeyFile: filepath.Join(dir, "secrets/host.key")}
	for name, v := range map[string]any{"deployment.json": metadata, "host.json": hostConfig} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"postgres-password", "master-key"} {
		if err := os.WriteFile(filepath.Join(dir, "secrets", name), []byte(strings.Repeat("a", 64)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets/host.key"), bytes.Repeat([]byte{0xbb}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, BundleTarget{DeploymentID: id}
}

func TestHostTargetRejectsUnfinishedDeployment(t *testing.T) {
	for _, marker := range []string{"install-incomplete", "restore-incomplete", "recovery-required.json"} {
		t.Run(marker, func(t *testing.T) {
			dir, target := hostTargetFixture(t)
			if err := os.WriteFile(filepath.Join(dir, "maintenance", marker), []byte("unfinished"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := HostTarget(dir, target); err == nil {
				t.Fatal("unfinished deployment accepted")
			}
		})
	}
}

func TestHostTargetRejectsUnsafeStoragePaths(t *testing.T) {
	for _, name := range []string{"socket", "blobs", "maintenance", "workspaces", "control"} {
		for _, variant := range []string{"missing", "symlink", "public"} {
			t.Run(name+"/"+variant, func(t *testing.T) {
				dir, target := hostTargetFixture(t)
				path := filepath.Join(dir, name)
				if variant == "public" {
					if err := os.Chmod(path, 0755); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.RemoveAll(path); err != nil {
						t.Fatal(err)
					}
					if variant == "symlink" {
						if err := os.Symlink(t.TempDir(), path); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err := HostTarget(dir, target); err == nil {
					t.Fatal("unsafe storage binding accepted")
				}
			})
		}
	}
}

func TestHostTargetRechecksStorageBindingAndRecovery(t *testing.T) {
	for _, variant := range []string{"socket", "blobs", "maintenance", "recovery", "workspaces", "control"} {
		t.Run(variant, func(t *testing.T) {
			dir, target := hostTargetFixture(t)
			config, err := HostTarget(dir, target)
			if err != nil {
				t.Fatal(err)
			}
			if variant == "recovery" {
				if err := os.WriteFile(filepath.Join(dir, "maintenance/restore-incomplete"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				p := filepath.Join(dir, variant)
				if err := os.Rename(p, p+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
				if variant == "workspaces" || variant == "control" {
					data, err := os.ReadFile(filepath.Join(p+"-original", "owner.json"))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(p, "owner.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := config.verifyTarget(); err == nil {
				t.Fatal("changed target accepted after configuration read")
			}
		})
	}
}

func TestHostTargetBindsPrivateOperatorPathsWithoutAmbientOverrides(t *testing.T) {
	dir, target := hostTargetFixture(t)
	t.Setenv("JUEX_DATABASE_URL", "postgres://wrong.example/production")
	t.Setenv("JUEX_MASTER_KEY", "must-not-be-used")
	got, err := HostTarget(dir, target)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "" || u.Path != "/juex" || u.Query().Get("host") != filepath.Join(dir, "socket") || got.BlobDirectory != filepath.Join(dir, "blobs") || got.MaintenanceDirectory != filepath.Join(dir, "maintenance") || got.Target != target || !bytes.Equal(got.MasterKey, bytes.Repeat([]byte{0xaa}, 32)) {
		t.Fatal("target path or secret binding changed")
	}
	printed, err := json.Marshal(got)
	if err != nil || string(printed) != "{}" {
		t.Fatal("operator secrets entered report")
	}
}

func TestHostTargetRejectsReboundOrUnsafeConfiguration(t *testing.T) {
	for _, variant := range []string{"different-id", "different-root", "host-mismatch", "duplicate", "public-secret", "secret-symlink", "malformed-key"} {
		t.Run(variant, func(t *testing.T) {
			dir, target := hostTargetFixture(t)
			switch variant {
			case "different-id":
				target.DeploymentID = "other"
			case "different-root", "duplicate":
				p := filepath.Join(dir, "deployment.json")
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if variant == "duplicate" {
					data = append([]byte(`{"format":2,`), data[1:]...)
				} else {
					var v map[string]any
					_ = json.Unmarshal(data, &v)
					v["root"] = "/different"
					data, _ = json.Marshal(v)
				}
				if err := os.WriteFile(p, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "host-mismatch":
				if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "public-secret":
				if err := os.Chmod(filepath.Join(dir, "secrets/master-key"), 0644); err != nil {
					t.Fatal(err)
				}
			case "secret-symlink":
				p := filepath.Join(dir, "secrets")
				if err := os.Rename(p, p+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p+"-real", p); err != nil {
					t.Fatal(err)
				}
			case "malformed-key":
				if err := os.WriteFile(filepath.Join(dir, "secrets/master-key"), []byte("private-invalid-value"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := HostTarget(dir, target)
			if err == nil {
				t.Fatal("unsafe target accepted")
			}
			if strings.Contains(err.Error(), "private-invalid-value") {
				t.Fatal("error leaked secret")
			}
		})
	}
}

func TestOfflineDatabaseRejectsAmbientPostgresConfiguration(t *testing.T) {
	for _, name := range []string{"PGPORT", "PGSERVICE", "PGSERVICEFILE", "PGOPTIONS", "PGHOST", "PGPASSFILE", "PGSSLMODE", "PGTZ", "PGTARGETSESSIONATTRS"} {
		t.Run(name, func(t *testing.T) {
			dir, target := hostTargetFixture(t)
			config, err := HostTarget(dir, target)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(name, "private-external-setting")
			_, err = offlineDatabaseConfig(config.DatabaseURL)
			if err == nil || !strings.Contains(err.Error(), "PG environment") || strings.Contains(err.Error(), "private-external-setting") {
				t.Fatalf("ambient database setting was not safely rejected: %v", err)
			}
		})
	}
}

func TestHostTargetDatabaseConfigurationUsesOwnedSocket(t *testing.T) {
	dir, target := hostTargetFixture(t)
	config, err := HostTarget(dir, target)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := offlineDatabaseConfig(config.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	c := parsed.ConnConfig
	if c.Host != filepath.Join(dir, "socket") || c.Port != 5432 || c.User != "juex" || c.Database != "juex" || c.Password != strings.Repeat("a", 64) || c.TLSConfig != nil || len(c.Fallbacks) != 0 || len(c.RuntimeParams) != 0 {
		t.Fatal("parsed connection differs from owned deployment")
	}
}
