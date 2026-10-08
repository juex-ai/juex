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
	value := management.ModelsImport{TenantID: tenant, Source: source, SourceSHA256: sourceSHA256}
	indexes := make(map[ModelKey]int, len(plan.Catalog))
	for i, model := range plan.Catalog {
		if _, exists := indexes[model.Key]; exists || model.Key.Provider != model.Configuration.Provider || model.Key.Name != model.Configuration.Name {
			return nil, management.ErrInvalid
		}
		indexes[model.Key] = i
		value.Models = append(value.Models, management.ImportedModel{Configuration: model.Configuration})
	}
	seen := make(map[ModelKey]bool, len(plan.Fallbacks))
	for _, tail := range plan.Fallbacks {
		i, exists := indexes[tail.Primary]
		if !exists || seen[tail.Primary] {
			return nil, management.ErrInvalid
		}
		seen[tail.Primary] = true
		for _, key := range tail.Candidates {
			value.Models[i].Fallbacks = append(value.Models[i].Fallbacks, management.ModelKey(key))
		}
	}
	ids, err := managed.ImportModels(ctx, directory, value)
	if err != nil {
		return nil, err
	}
	result := make(map[ModelKey]management.ModelImportIdentity, len(ids))
	for key, id := range ids {
		result[ModelKey(key)] = id
	}
	return result, nil
}
