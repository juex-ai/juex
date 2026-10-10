package postgres

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/importproof"
)

type importedModelIdentity struct {
	Key management.ModelKey `json:"key"`
	management.ModelImportIdentity
}

// ModelImportProofVersion is an offline routing hint. The import/recovery
// transaction rechecks the version, source and complete payload under its lock.
func (d *Directory) ModelImportProofVersion(ctx context.Context, tenant string) (int, error) {
	var version int
	err := d.pool.QueryRow(ctx, `SELECT proof_version FROM management.model_imports WHERE singleton AND tenant_id=$1`, tenant).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return version, err
}

// ImportModels is an offline operator boundary, not a Fleet or HTTP operation.
// Configuration, tenant access and recovery identity commit
// together. An exact retry only returns identities; it never undoes later edits.
func (d *Directory) ImportModels(ctx context.Context, value management.ModelsImport) (map[management.ModelKey]management.ModelImportIdentity, error) {
	value, hash, err := prepareModelImport(value)
	if err != nil {
		return nil, err
	}
	return d.importModels(ctx, value, hash, 2)
}

// RecoverModelImportV1 verifies historical private input without current adapter
// validation or any attempt to enable, configure or create a model.
func (d *Directory) RecoverModelImportV1(ctx context.Context, proof importproof.ModelsV1) (map[management.ModelKey]management.ModelImportIdentity, error) {
	value, hash, err := proof.Prepare()
	if err != nil {
		return nil, err
	}
	return d.importModels(ctx, value, hash, 1)
}

func (d *Directory) importModels(ctx context.Context, value management.ModelsImport, hash string, version int) (map[management.ModelKey]management.ModelImportIdentity, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.management.models'))`); err != nil {
		return nil, err
	}
	if err := lockTenant(ctx, tx, value.TenantID); err != nil {
		return nil, err
	}
	var proofVersion int
	var tenant, source, sourceHash, payloadHash string
	var receipt []importedModelIdentity
	err = tx.QueryRow(ctx, `SELECT tenant_id,source,source_sha256,payload_sha256,models,proof_version FROM management.model_imports WHERE singleton`).Scan(&tenant, &source, &sourceHash, &payloadHash, &receipt, &proofVersion)
	if err == nil {
		if version == 2 && proofVersion == 1 {
			return nil, management.ErrImportProofVersion
		}
		if proofVersion != version {
			return nil, management.ErrConflict
		}
		if tenant != value.TenantID || source != value.Source || sourceHash != value.SourceSHA256 || payloadHash != hash || len(receipt) != len(value.Models) {
			return nil, management.ErrConflict
		}
		ids := make(map[management.ModelKey]management.ModelImportIdentity, len(receipt))
		seen := map[string]bool{}
		for i, item := range receipt {
			config := value.Models[i].Configuration
			id, parseErr := uuid.Parse(item.ID)
			if item.Key != (management.ModelKey{Provider: config.Provider, Name: config.Name}) || parseErr != nil || id == uuid.Nil || id.String() != item.ID || seen[item.ID] || item.ModelAuthorizationEpoch < 1 {
				return nil, management.ErrConflict
			}
			seen[item.ID] = true
			ids[item.Key] = item.ModelImportIdentity
		}
		// This receipt grants no authority. Callers must admit every subsequent
		// Agent import and model request against current Management policy.
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return ids, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if version != 2 {
		return nil, management.ErrConflict
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.models) OR EXISTS(SELECT 1 FROM management.tenant_model_policy WHERE tenant_id=$1)`, value.TenantID).Scan(&used); err != nil {
		return nil, err
	}
	if used {
		return nil, management.ErrConflict
	}
	ids := make(map[management.ModelKey]management.ModelImportIdentity, len(value.Models))
	for _, item := range value.Models {
		config := item.Configuration
		id := uuid.NewString()
		keyCipher, err := d.config.Secrets.Seal("model:"+id, []byte(config.APIKey))
		if err != nil {
			return nil, err
		}
		options, err := json.Marshal(config.Options)
		if err != nil {
			return nil, management.ErrInvalid
		}
		optionsCipher, err := d.config.Secrets.Seal("model-options:"+id, options)
		if err != nil {
			return nil, err
		}
		var epoch int64
		if err := tx.QueryRow(ctx, `INSERT INTO management.models(id,provider,name,protocol,endpoint,key_cipher,context_window,max_output,output_reserve,enabled,options_cipher) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING authorization_epoch`, id, config.Provider, config.Name, config.Protocol, config.Endpoint, keyCipher, config.ContextWindow, config.MaxOutput, config.OutputReserve, config.Enabled, optionsCipher).Scan(&epoch); err != nil {
			return nil, classify(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO management.operator_audit(action,resource_id) VALUES('model.imported',$1)`, id); err != nil {
			return nil, err
		}
		key := management.ModelKey{Provider: config.Provider, Name: config.Name}
		identity := management.ModelImportIdentity{ID: id, ModelAuthorizationEpoch: epoch}
		ids[key] = identity
		receipt = append(receipt, importedModelIdentity{Key: key, ModelImportIdentity: identity})
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.tenant_model_policy(tenant_id,inherit) VALUES($1,false)`, value.TenantID); err != nil {
		return nil, classify(err)
	}
	for _, item := range value.Models {
		id := ids[management.ModelKey{Provider: item.Configuration.Provider, Name: item.Configuration.Name}].ID
		if _, err := tx.Exec(ctx, `INSERT INTO management.tenant_model_access(tenant_id,model_id) VALUES($1,$2)`, value.TenantID, id); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.operator_audit(action,resource_id) VALUES('model.catalog_imported',$1)`, value.TenantID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.model_imports(tenant_id,source,source_sha256,payload_sha256,models,proof_version) VALUES($1,$2,$3,$4,$5,2)`, value.TenantID, value.Source, value.SourceSHA256, hash, receipt); err != nil {
		return nil, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

func prepareModelImport(value management.ModelsImport) (management.ModelsImport, string, error) {
	tenant, err := uuid.Parse(value.TenantID)
	if err != nil || tenant == uuid.Nil || tenant.String() != value.TenantID {
		return value, "", management.ErrInvalid
	}
	sourceHash, err := hex.DecodeString(value.SourceSHA256)
	if err != nil || len(sourceHash) != 32 || strings.ToLower(value.SourceSHA256) != value.SourceSHA256 || strings.TrimSpace(value.Source) == "" || len(value.Source) > 512 || !utf8.ValidString(value.Source) || strings.ContainsRune(value.Source, 0) || len(value.Models) == 0 || len(value.Models) > 1024 {
		return value, "", management.ErrInvalid
	}
	value.Models = slices.Clone(value.Models)
	keys := make(map[management.ModelKey]bool, len(value.Models))
	for i, item := range value.Models {
		if !validImportedModelText(item.Configuration) {
			return value, "", management.ErrInvalid
		}
		config, _, err := prepareModelConfiguration(item.Configuration)
		if err != nil {
			return value, "", err
		}
		key := management.ModelKey{Provider: config.Provider, Name: config.Name}
		if keys[key] {
			return value, "", management.ErrInvalid
		}
		keys[key] = true
		value.Models[i].Configuration = config
	}
	slices.SortFunc(value.Models, func(a, b management.ImportedModel) int {
		if order := cmp.Compare(a.Configuration.Provider, b.Configuration.Provider); order != 0 {
			return order
		}
		return cmp.Compare(a.Configuration.Name, b.Configuration.Name)
	})
	// Hash the complete private input, including keys and options. The public
	// App plan intentionally omits configuration and cannot identify this write.
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64<<20 {
		return value, "", management.ErrInvalid
	}
	var detached management.ModelsImport
	if err := json.Unmarshal(encoded, &detached); err != nil {
		return value, "", management.ErrInvalid
	}
	hash := sha256.Sum256(append([]byte("juex.model-import/2\x00"), encoded...))
	return detached, hex.EncodeToString(hash[:]), nil
}

func validImportedModelText(config management.ModelConfiguration) bool {
	text := []string{config.Provider, config.Name, config.Endpoint, config.APIKey, string(config.Protocol), config.Options.Authentication, config.Options.ThinkingEffort, config.Options.Compat.CodexTransport, config.Options.Compat.MaxTokensField}
	text = append(text, config.Options.Compat.ReasoningReplayFields...)
	for _, values := range []map[string]string{config.Options.Headers, config.Options.Query} {
		for key, value := range values {
			text = append(text, key, value)
		}
	}
	for _, value := range text {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return !strings.ContainsRune(config.Provider, 0) && !strings.ContainsRune(config.Name, 0)
}
