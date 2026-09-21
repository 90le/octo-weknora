package types

const (
	// DataSourceDeleteModeDetach keeps the established meaning of deleting a
	// source: remove its configuration and source cache while retaining the
	// content that was previously imported into the knowledge base.
	DataSourceDeleteModeDetach = "detach"
	// DataSourceDeleteModePurgeGenerated removes only the generated knowledge
	// proven to belong to this source, after a fresh signed preview.
	DataSourceDeleteModePurgeGenerated = "purge_generated"
)

// DataSourceDeleteRequest is deliberately separate from the normal DELETE
// /datasource/:id operation. A purge requires both the explicit mode and a
// short-lived preview token. Keeping this explicit prevents a future UI or API
// client from turning the long-standing configuration-only delete into
// destructive content removal.
type DataSourceDeleteRequest struct {
	Mode         string `json:"mode"`
	PreviewToken string `json:"preview_token,omitempty"`
}

// DataSourceDeletePreview describes the exact active knowledge entries a
// source purge is permitted to remove. It never exposes source credentials or
// content bodies.
//
// SharedOrUnverifiableResourcesCount is intentionally conservative. It counts
// source file references that require reference-safe cleanup or lack a durable
// ownership proof. LegacyUnverifiableResourcesCount is the blocking subset:
// purge_generated refuses to run while it is non-zero, leaving the source and
// its imported knowledge untouched for an audited migration or cleanup plan.
type DataSourceDeletePreview struct {
	DataSourceID                       string                                `json:"datasource_id"`
	GeneratedKnowledgeCount            int                                   `json:"generated_knowledge_count"`
	GeneratedStorageBytes              int64                                 `json:"generated_storage_bytes"`
	SharedOrUnverifiableResourcesCount int                                   `json:"shared_or_unverifiable_resources_count"`
	LegacyUnverifiableResourcesCount   int                                   `json:"legacy_unverifiable_resources_count"`
	PreviewToken                       string                                `json:"preview_token"`
	ExpiresAt                          string                                `json:"expires_at"`
	KnowledgeSamples                   []DataSourceGeneratedContentPurgeItem `json:"knowledge_samples,omitempty"`
	LegacyFileSamples                  []DataSourceGeneratedContentPurgeFile `json:"legacy_file_samples,omitempty"`
}

// DataSourceGeneratedContentPurgeItem is a bounded, display-safe preview of a
// knowledge entry. IDs are included so an admin can audit the exact effect.
type DataSourceGeneratedContentPurgeItem struct {
	KnowledgeID string `json:"knowledge_id"`
	Title       string `json:"title"`
	FileName    string `json:"file_name,omitempty"`
	Type        string `json:"type"`
}

// DataSourceGeneratedContentPurgeFile lists a source knowledge entry that
// needs review in a deletion preview. It intentionally never exposes a raw
// provider or host path: these previews are available through the normal UI
// and must not disclose server storage topology.
type DataSourceGeneratedContentPurgeFile struct {
	KnowledgeID string `json:"knowledge_id"`
	Title       string `json:"title"`
	FileName    string `json:"file_name,omitempty"`
	Status      string `json:"status"`
}

// DataSourceDeleteResult records the completed explicit source deletion.
type DataSourceDeleteResult struct {
	DataSourceID      string `json:"datasource_id"`
	Mode              string `json:"mode"`
	PurgedKnowledge   int    `json:"purged_knowledge"`
	DataSourceDeleted bool   `json:"data_source_deleted"`
}
