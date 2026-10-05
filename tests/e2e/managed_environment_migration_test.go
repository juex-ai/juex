//go:build postgres

package e2e

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
)

func TestManagedEnvironmentMigrationPreservesHostedEnrollmentAndQuotaIdentity(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `CREATE SCHEMA execution; CREATE TABLE execution.schema_versions(version integer PRIMARY KEY,checksum text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "../../internal/execution/postgres")
	// These immutable migrations define the deployed pre-Host database fixture.
	previous := []string{"schema.sql", "events_schema.sql", "hosted_schema.sql", "hosted_storage_schema.sql", "observed_output_schema.sql", "artifacts_schema.sql", "transfers_schema.sql", "cancellations_schema.sql", "transfer_progress_schema.sql", "purge_schema.sql", "retention_schema.sql", "recovery_schema.sql", "default_environment_schema.sql"}
	for i, name := range previous {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(name, err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO execution.schema_versions VALUES($1,$2)`, i+1, fmt.Sprintf("%x", sha256.Sum256(data))); err != nil {
			t.Fatal(err)
		}
	}
	environment, agent, storage := uuid.NewString(), uuid.NewString(), uuid.NewString()
	credential := fmt.Sprintf("%x", sha256.Sum256([]byte("existing enrollment")))
	if _, err := f.pool.Exec(ctx, `INSERT INTO execution.environments(id,tenant_id,user_id,fleet_id,kind,name,os,working_directory,credential_hash,removal_epoch,grants,ceiling,journal_id) VALUES($1,$2,$3,$4,'hosted','Hosted workspace','linux','/workspace',$5,0,'{}','{}','retained-journal')`, environment, uuid.NewString(), uuid.NewString(), uuid.NewString(), credential); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO execution.hosted(environment_id,agent_id,slot,memory_bytes,nano_cpus,storage_identity,workspace_bytes,workspace_inodes,provisioned) VALUES($1,$2,17,805306368,1000000000,$3,2147483648,131072,true)`, environment, agent, storage); err != nil {
		t.Fatal(err)
	}
	deletedHosted, pairedNative := uuid.NewString(), uuid.NewString()
	for _, old := range []struct{ id, kind string }{{deletedHosted, "hosted"}, {pairedNative, "native"}} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO execution.environments(id,tenant_id,user_id,fleet_id,kind,name,os,working_directory,credential_hash,removal_epoch,grants,ceiling,status) VALUES($1,$2,$3,$4,$5,'retained','linux','', $1::uuid::text,0,'{}','{}','revoked')`, old.id, uuid.NewString(), uuid.NewString(), uuid.NewString(), old.kind); err != nil {
			t.Fatal(err)
		}
	}
	var project int64
	if err := f.pool.QueryRow(ctx, `SELECT project_id FROM execution.hosted WHERE environment_id=$1`, environment).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `SELECT setval(pg_get_serial_sequence('execution.hosted','project_id'),777,true)`); err != nil {
		t.Fatal(err)
	}
	if err := executionpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	if err := executionpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal("migration is not repeatable", err)
	}
	var gotCredential, journal, gotStorage, backend, home string
	var gotProject, nextProject int64
	var slot int
	var provisioned, managed bool
	if err := f.pool.QueryRow(ctx, `SELECT e.credential_hash,e.journal_id,m.storage_identity::text,m.project_id,m.slot,m.provisioned,m.backend,m.home_directory,e.managed FROM execution.environments e JOIN execution.managed_environments m ON m.environment_id=e.id WHERE e.id=$1`, environment).Scan(&gotCredential, &journal, &gotStorage, &gotProject, &slot, &provisioned, &backend, &home, &managed); err != nil {
		t.Fatal(err)
	}
	if gotCredential != credential || journal != "retained-journal" || gotStorage != storage || gotProject != project || slot != 17 || !provisioned || !managed || backend != "gvisor" || home != "/home/agent" {
		t.Fatal("migration replaced Hosted ownership", gotCredential == credential, journal, gotStorage, gotProject, slot, provisioned, backend, home)
	}
	if err := f.pool.QueryRow(ctx, `SELECT nextval('execution.managed_project_id')`).Scan(&nextProject); err != nil || nextProject != 778 {
		t.Fatal("migration reused a previously issued project ID", nextProject, err)
	}
	for _, retained := range []struct {
		id      string
		managed bool
	}{{deletedHosted, true}, {pairedNative, false}} {
		var got bool
		if err := f.pool.QueryRow(ctx, `SELECT managed FROM execution.environments WHERE id=$1`, retained.id).Scan(&got); err != nil || got != retained.managed {
			t.Fatal("migration changed retained environment ownership", retained, got, err)
		}
	}
}
