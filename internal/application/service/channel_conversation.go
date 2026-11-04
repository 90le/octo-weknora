package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// ReadChannelConversation reads the local transcript only for a bound web user.
func (s *messageService) ReadChannelConversation(
	ctx context.Context, session, anchor string, position, page int,
) ([]types.ChannelConversationMessage, int, bool, error) {
	user := artifactWebOwnerID(ctx)
	repo, ok := s.messageRepo.(interface {
		ReadChannelConversation(
			context.Context, uint64, string, string, string, int, int,
		) ([]types.ChannelConversationMessage, int, bool, error)
	})
	if user == "" || !ok {
		return nil, 0, false, gorm.ErrRecordNotFound
	}
	return repo.ReadChannelConversation(ctx, types.MustTenantIDFromContext(ctx), user, session, anchor, position, page)
}
