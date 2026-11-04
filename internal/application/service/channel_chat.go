package service

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

func (s *messageService) ReadChannelChat(
	ctx context.Context, id string, limit int, before *time.Time, beforeID string,
) (*types.Session, []*types.Message, error) {
	user := artifactWebOwnerID(ctx)
	repo, ok := s.messageRepo.(interface {
		ReadChannelChat(
			context.Context, uint64, string, string, int, *time.Time, string,
		) (*types.Session, []*types.Message, error)
	})
	if user == "" || !ok {
		return nil, nil, gorm.ErrRecordNotFound
	}
	return repo.ReadChannelChat(ctx, types.MustTenantIDFromContext(ctx), user, id, limit, before, beforeID)
}

// ChannelChatKnowledge is used only for resolving a persisted reference's owner;
// the HTTP adapter must independently authorize its KB before exposing it.
func (s *messageService) ChannelChatKnowledge(ctx context.Context, id string) (*types.Knowledge, error) {
	if artifactWebOwnerID(ctx) == "" || s.knowService == nil || id == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return s.knowService.GetKnowledgeByIDOnly(ctx, id)
}
