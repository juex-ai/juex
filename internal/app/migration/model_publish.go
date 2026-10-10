package migration

import (
	"context"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/postgres"
)

// PublishModels initializes a fresh deployment through Management's offline
// operator boundary. The caller supplies verified source identity; this method
// does not read source credentials or authorize subsequent Agent imports.
func PublishModels(ctx context.Context, directory *postgres.Directory, tenant, source, sourceSHA256 string, plan ModelPublicationPlan) (map[ModelKey]management.ModelImportIdentity, error) {
	ids, _, err := publishModels(ctx, directory, tenant, source, sourceSHA256, plan)
	return ids, err
}

func publishModels(ctx context.Context, directory *postgres.Directory, tenant, source, sourceSHA256 string, plan ModelPublicationPlan) (map[ModelKey]management.ModelImportIdentity, bool, error) {
	value := management.ModelsImport{TenantID: tenant, Source: source, SourceSHA256: sourceSHA256}
	indexes := make(map[ModelKey]int, len(plan.Catalog))
	for i, model := range plan.Catalog {
		if _, exists := indexes[model.Key]; exists || model.Key.Provider != model.Configuration.Provider || model.Key.Name != model.Configuration.Name {
			return nil, false, management.ErrInvalid
		}
		indexes[model.Key] = i
		value.Models = append(value.Models, management.ImportedModel{Configuration: model.Configuration})
	}
	version, err := directory.ModelImportProofVersion(ctx, tenant)
	if err != nil {
		return nil, false, err
	}
	var ids map[management.ModelKey]management.ModelImportIdentity
	if version == 1 {
		proof, proofErr := modelProofV1(value, plan)
		if proofErr != nil {
			return nil, false, proofErr
		}
		ids, err = managed.RecoverModelsV1(ctx, directory, proof)
	} else {
		ids, err = managed.ImportModels(ctx, directory, value)
	}
	if err != nil {
		return nil, false, err
	}
	result := make(map[ModelKey]management.ModelImportIdentity, len(ids))
	for key, id := range ids {
		result[ModelKey(key)] = id
	}
	return result, version == 1, nil
}
