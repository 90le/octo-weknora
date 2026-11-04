package service

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
)

// ArtifactUsage reports the logical size of visible artifact version records.
func (s *messageService) ArtifactUsage(ctx context.Context) (int64, int64, error) {
	repo, ok := s.messageRepo.(interface {
		ArtifactUsage(context.Context, *types.ArtifactLibraryQuery) (int64, int64, error)
	})
	if !ok {
		return 0, 0, errors.New("artifact usage unavailable")
	}
	return repo.ArtifactUsage(ctx, &types.ArtifactLibraryQuery{
		TenantID:   types.MustTenantIDFromContext(ctx),
		UserID:     types.SessionOwnerIDFromContext(ctx),
		WebOwnerID: artifactWebOwnerID(ctx),
	})
}
