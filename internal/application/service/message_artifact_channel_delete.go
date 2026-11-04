package service

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// DeleteChannelArtifact uses the dedicated channel grant and existing safe reclamation.
func (s *messageService) DeleteChannelArtifact(
	ctx context.Context, req *types.ArtifactDeleteRequest, tokenID string, expiry time.Time,
) (*types.ArtifactDeleteResult, error) {
	user := artifactWebOwnerID(ctx)
	if user == "" || tokenID == "" || req == nil || req.SessionID == "" || req.MessageID == "" || req.Index < 0 {
		return nil, ErrArtifactNotFound
	}
	repo, ok := s.messageRepo.(interface {
		DeleteChannelArtifact(
			context.Context, uint64, string, string, time.Time, *types.ArtifactDeleteRequest,
		) ([]types.MessageArtifactRecord, []types.ArtifactRef, error)
	})
	if !ok {
		return nil, ErrArtifactNotFound
	}
	rows, marked, err := repo.DeleteChannelArtifact(ctx, types.MustTenantIDFromContext(ctx), user, tokenID, expiry, req)
	if err != nil {
		return nil, err
	}
	var name string
	for _, row := range rows {
		if row.MessageID == req.MessageID && row.Position == req.Index {
			name = row.FileName
			break
		}
	}
	return &types.ArtifactDeleteResult{
		FileName: name, Deleted: len(marked), Reclaim: s.reclaimableBlobs(ctx, artifactReclaimList(rows, marked)),
	}, nil
}
