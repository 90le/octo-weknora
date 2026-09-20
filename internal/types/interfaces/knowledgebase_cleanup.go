package interfaces

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
)

// KBDeletionResource retains retry evidence until physical cleanup and metadata
// retirement have both succeeded. Shared resources retain their bytes/catalog row.
type KBDeletionResource struct {
	Resource types.StoredResource
	Shared   bool
}
type KBDeletionPlan struct {
	Knowledge []*types.Knowledge
	Resources []KBDeletionResource
}
type KnowledgeBaseCleanupRepository interface {
	PrepareKnowledgeBaseCleanup(context.Context, uint64, string) (*KBDeletionPlan, error)
	FinalizeKnowledgeBaseCleanup(context.Context, uint64, string) error
}
