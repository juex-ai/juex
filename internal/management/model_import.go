package management

// ModelKey is the deployment catalog identity, independent of generated UUIDs.
type ModelKey struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

// ImportedModel carries private operator input, never a public read model.
// Fallbacks are direct, ordered references within this import's catalog.
type ImportedModel struct {
	Configuration ModelConfiguration `json:"configuration"`
	Fallbacks     []ModelKey         `json:"fallbacks"`
}

// ModelsImport initializes an empty deployment catalog and an explicit Tenant
// allowlist. It cannot merge existing configuration or set a platform default.
// Only the offline deployment operator may call this boundary.
type ModelsImport struct {
	TenantID     string          `json:"tenant_id"`
	Source       string          `json:"source"`
	SourceSHA256 string          `json:"source_sha256"`
	Models       []ImportedModel `json:"models"`
}
