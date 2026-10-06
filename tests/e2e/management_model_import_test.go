//go:build postgres

package e2e

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func modelImportRequest(tenant string) management.ModelsImport {
	v := management.ModelsImport{TenantID: tenant, Source: "fixture", SourceSHA256: strings.Repeat("a", 64)}
	for _, name := range []string{"a", "b", "c"} {
		v.Models = append(v.Models, management.ImportedModel{Configuration: management.ModelConfiguration{Provider: "fixture", Name: name, Endpoint: "https://example.test/v1", APIKey: "private-fixture-key", Protocol: llm.ProtocolOpenAIChat, ContextWindow: 32768, OutputReserve: 8192, Enabled: true, Options: management.ModelOptions{Headers: map[string]string{"X-Fixture": "private-fixture-header"}, Query: map[string]string{"route": "private-fixture-query"}}}})
	}
	v.Models[0].Fallbacks = []management.ModelKey{{Provider: "fixture", Name: "b"}, {Provider: "fixture", Name: "c"}}
	return v
}

func modelImportCounts(t *testing.T, pool *pgxpool.Pool) [6]int {
	t.Helper()
	var counts [6]int
	err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM management.models),(SELECT count(*) FROM management.model_fallbacks),(SELECT count(*) FROM management.tenant_model_policy),(SELECT count(*) FROM management.tenant_model_access),(SELECT count(*) FROM management.model_imports),(SELECT count(*) FROM management.operator_audit WHERE action LIKE 'model.%')`).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5])
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestManagementModelImportRetryKeepsPrivateChangesAndFreshAuthority(t *testing.T) {
	pool, d := managementDatabase(t)
	user, tenant, _ := agentImportOwner(t, d)
	ctx := context.Background()
	value := modelImportRequest(tenant.ID)
	ids, err := managed.ImportModels(ctx, d, value)
	if err != nil {
		t.Fatal(err)
	}
	if got := modelImportCounts(t, pool); got != [6]int{3, 2, 1, 3, 1, 4} {
		t.Fatal("incomplete publication", got)
	}
	// A lost response recovers the committed identities without creating rows.
	recovered := managed.RuntimeAuthority{Directory: d}
	again, err := managed.ImportModels(ctx, d, value)
	if err != nil || !reflect.DeepEqual(ids, again) || modelImportCounts(t, pool) != [6]int{3, 2, 1, 3, 1, 4} {
		t.Fatal("retry changed identities or created records", err)
	}
	var receipt string
	if err := pool.QueryRow(ctx, `SELECT row_to_json(r)::text FROM management.model_imports r`).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(receipt, "private-fixture") || strings.Contains(receipt, "example.test") {
		t.Fatal("receipt contains private model configuration")
	}
	var plainSecret bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.models WHERE position($1::bytea IN key_cipher)>0 OR position($2::bytea IN options_cipher)>0)`, []byte("private-fixture-key"), []byte("private-fixture-header")).Scan(&plainSecret); err != nil || plainSecret {
		t.Fatal("model secrets were not sealed", err)
	}
	other, err := d.CreateTenant(ctx, "Other import target", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*management.ModelsImport){
		"key":            func(v *management.ModelsImport) { v.Models[0].Configuration.APIKey += "changed" },
		"headers":        func(v *management.ModelsImport) { v.Models[0].Configuration.Options.Headers["X-Fixture"] += "changed" },
		"query":          func(v *management.ModelsImport) { v.Models[0].Configuration.Options.Query["route"] += "changed" },
		"endpoint":       func(v *management.ModelsImport) { v.Models[0].Configuration.Endpoint += "/changed" },
		"reserve":        func(v *management.ModelsImport) { v.Models[0].Configuration.OutputReserve++ },
		"fallback order": func(v *management.ModelsImport) { slices.Reverse(v.Models[0].Fallbacks) },
		"tenant":         func(v *management.ModelsImport) { v.TenantID = other.ID },
		"source":         func(v *management.ModelsImport) { v.Source += "changed" },
		"source hash":    func(v *management.ModelsImport) { v.SourceSHA256 = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			v := modelImportRequest(tenant.ID)
			mutate(&v)
			got, err := managed.ImportModels(ctx, d, v)
			if !errors.Is(err, management.ErrConflict) || got != nil {
				t.Fatal("different private input accepted", err)
			}
		})
	}
	primary := ids[management.ModelKey{Provider: "fixture", Name: "a"}]
	agent, err := d.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Imported", ModelID: primary})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := recovered.Authorize(ctx, user.ID, tenant.ID, agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := recovered.Snapshot(ctx, scope)
	if err != nil || len(plan.Models) != 3 {
		t.Fatal("imported direct fallback plan unavailable", err)
	}
	rotated := value.Models[0].Configuration
	rotated.APIKey = "rotated-fixture-key"
	if _, err := managed.ConfigureModel(ctx, d, rotated); err != nil {
		t.Fatal(err)
	}
	if err := d.SetModelFallbacks(ctx, primary, nil); err != nil {
		t.Fatal(err)
	}
	before := modelImportCounts(t, pool)
	again, err = managed.ImportModels(ctx, d, value)
	if err != nil || !reflect.DeepEqual(ids, again) || before != modelImportCounts(t, pool) {
		t.Fatal("retry reset later operator changes", err)
	}
	profile, err := recovered.Profile(ctx, scope, plan.Models[0])
	if err != nil || profile.APIKey != rotated.APIKey {
		t.Fatal("retry restored source credential", err)
	}
	if err := d.SetModelEnabled(ctx, primary, false); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.ImportModels(ctx, d, value); err != nil {
		t.Fatal("identity retry requires old model authorization", err)
	}
	if _, err := recovered.Profile(ctx, scope, plan.Models[0]); !errors.Is(err, managedruntime.ErrModelUnavailable) {
		t.Fatal("disabled model became authorized through receipt", err)
	}
	if err := d.SetModelEnabled(ctx, primary, true); err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Profile(ctx, scope, plan.Models[0]); !errors.Is(err, managedruntime.ErrModelUnavailable) {
		t.Fatal("disable/restore revived stale grant", err)
	}
	if err := d.SetTenantModels(ctx, tenant.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	before = modelImportCounts(t, pool)
	if _, err := managed.ImportModels(ctx, d, value); err != nil || before != modelImportCounts(t, pool) {
		t.Fatal("retry changed tenant revocation", err)
	}
	if _, err := recovered.Snapshot(ctx, scope); !errors.Is(err, managedruntime.ErrModelUnavailable) {
		t.Fatal("receipt bypassed current tenant policy", err)
	}
}

func TestManagementModelImportRollsBackEveryOwnerWrite(t *testing.T) {
	pool, d := managementDatabase(t)
	_, tenant, _ := agentImportOwner(t, d)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE FUNCTION management.reject_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture receipt failure'; END $$;
CREATE TRIGGER reject_import BEFORE INSERT ON management.model_imports FOR EACH ROW EXECUTE FUNCTION management.reject_import()`)
	if err != nil {
		t.Fatal(err)
	}
	value := modelImportRequest(tenant.ID)
	ids, err := managed.ImportModels(ctx, d, value)
	if err == nil || ids != nil || modelImportCounts(t, pool) != [6]int{} {
		t.Fatal("partial publication survived receipt failure", err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_import ON management.model_imports`); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.ImportModels(ctx, d, value); err != nil || modelImportCounts(t, pool) != [6]int{3, 2, 1, 3, 1, 4} {
		t.Fatal("rolled back import could not be retried", err)
	}
}

func TestManagementModelImportRejectsUsedTargetsAndInvalidAdapters(t *testing.T) {
	for _, kind := range []string{"catalog", "explicit inherit", "explicit empty", "invalid adapter"} {
		t.Run(kind, func(t *testing.T) {
			pool, d := managementDatabase(t)
			_, tenant, _ := agentImportOwner(t, d)
			ctx := context.Background()
			value := modelImportRequest(tenant.ID)
			want := management.ErrConflict
			switch kind {
			case "catalog":
				if _, err := managed.ConfigureModel(ctx, d, value.Models[0].Configuration); err != nil {
					t.Fatal(err)
				}
			case "explicit inherit", "explicit empty":
				if err := d.SetTenantModels(ctx, tenant.ID, kind == "explicit inherit", nil); err != nil {
					t.Fatal(err)
				}
			case "invalid adapter":
				value.Models[1].Configuration.Options.Compat.MaxTokensField = "private-unsupported-field"
				want = management.ErrInvalid
			}
			before := modelImportCounts(t, pool)
			if ids, err := managed.ImportModels(ctx, d, value); !errors.Is(err, want) || ids != nil || before != modelImportCounts(t, pool) {
				t.Fatal("import overwrote target or partially published invalid input", err)
			}
		})
	}
}

func waitModelImportLock(t *testing.T, pool *pgxpool.Pool, query string, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query=$1`, query).Scan(&waiting); err != nil {
			t.Fatal("lock waiter observation failed", err)
		}
		if waiting >= count {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("operator did not reach the shared lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestManagementModelImportSerializesWithOrdinaryOperators(t *testing.T) {
	for _, kind := range []string{"configure", "policy", "same import"} {
		t.Run(kind, func(t *testing.T) {
			pool, d := managementDatabase(t)
			_, tenant, _ := agentImportOwner(t, d)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			value := modelImportRequest(tenant.ID)
			gate, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback(context.Background()) }()
			lock := `SELECT pg_advisory_xact_lock(hashtext('juex.management.models'))`
			if kind == "policy" {
				lock = `SELECT id FROM management.tenants WHERE id=$1 FOR UPDATE`
				_, err = gate.Exec(ctx, lock, tenant.ID)
			} else {
				_, err = gate.Exec(ctx, lock)
			}
			if err != nil {
				t.Fatal(err)
			}
			first := make(chan error, 1)
			var firstIDs map[management.ModelKey]string
			go func() {
				var err error
				switch kind {
				case "configure":
					config := value.Models[0].Configuration
					config.APIKey = "operator-won-key"
					_, err = managed.ConfigureModel(ctx, d, config)
				case "policy":
					err = d.SetTenantModels(ctx, tenant.ID, false, nil)
				default:
					firstIDs, err = managed.ImportModels(ctx, d, value)
				}
				first <- err
			}()
			waitModelImportLock(t, pool, lock, 1)
			second := make(chan error, 1)
			var secondIDs map[management.ModelKey]string
			go func() {
				var err error
				secondIDs, err = managed.ImportModels(ctx, d, value)
				second <- err
			}()
			waitModelImportLock(t, pool, lock, 2)
			if err := gate.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-first; err != nil {
				t.Fatal("first operator failed", err)
			}
			err = <-second
			switch kind {
			case "same import":
				if err != nil || !reflect.DeepEqual(firstIDs, secondIDs) || modelImportCounts(t, pool) != [6]int{3, 2, 1, 3, 1, 4} {
					t.Fatal("concurrent retry changed publication", err)
				}
			case "configure":
				if !errors.Is(err, management.ErrConflict) || modelImportCounts(t, pool) != [6]int{1, 0, 0, 0, 0, 1} {
					t.Fatal("import overwrote concurrent catalog", err)
				}
			case "policy":
				if !errors.Is(err, management.ErrConflict) || modelImportCounts(t, pool) != [6]int{0, 0, 1, 0, 0, 1} {
					t.Fatal("import overwrote concurrent tenant policy", err)
				}
			}
		})
	}
}

func TestManagementModelImportCodexCanonicalAccount(t *testing.T) {
	pool, d := managementDatabase(t)
	_, tenant, _ := agentImportOwner(t, d)
	value := modelImportRequest(tenant.ID)
	value.Models = value.Models[:1]
	value.Models[0].Fallbacks = nil
	config := &value.Models[0].Configuration
	config.Provider, config.Protocol = "openai-codex", llm.ProtocolOpenAICodexResponses
	config.Endpoint = "https://chatgpt.com/backend-api/codex"
	config.Options.Headers = map[string]string{"CHATGPT-ACCOUNT-ID": "  explicit-fixture-account  "}
	first, err := managed.ImportModels(context.Background(), d, value)
	if err != nil {
		t.Fatal(err)
	}
	config.Options.Headers = map[string]string{"ChatGPT-Account-ID": "explicit-fixture-account"}
	again, err := managed.ImportModels(context.Background(), d, value)
	if err != nil || !reflect.DeepEqual(first, again) || modelImportCounts(t, pool) != [6]int{1, 0, 1, 1, 1, 2} {
		t.Fatal("canonical account retry changed import", err)
	}
}
