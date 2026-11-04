package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// Count every mapping, including retired and other-tenant rows: ambiguous
// provenance must not be resolved using whichever grant happens to match.
const unambiguousChannelSession = `(SELECT COUNT(*) FROM im_channel_sessions provenance
 WHERE provenance.session_id = s.id) = 1`

// ReadChannelConversation checks the grant and live artifact in the same SQL
// statement as the transcript read. It never exposes system or tool fields.
func (r *messageRepository) ReadChannelConversation(
	ctx context.Context, tenant uint64, user, session, anchor string, position, page int,
) ([]types.ChannelConversationMessage, int, bool, error) {
	if tenant == 0 || user == "" || session == "" || anchor == "" || position < 0 || page < 0 || page > 10000 {
		return nil, 0, false, gorm.ErrRecordNotFound
	}
	if page == 0 {
		var count int64
		err := r.db.WithContext(ctx).Table("messages m").
			Joins("JOIN sessions s ON s.id = m.session_id AND s.deleted_at IS NULL").
			Joins("JOIN messages anchor ON anchor.id = ? AND anchor.session_id = s.id "+
				"AND anchor.role = ? AND anchor.deleted_at IS NULL", anchor, "assistant").
			Where("s.tenant_id = ? AND s.id = ? AND m.deleted_at IS NULL AND m.role IN ? AND "+
				channelArtifactGrant+" AND "+unambiguousChannelSession+" AND (m.created_at < anchor.created_at OR "+
				"(m.created_at = anchor.created_at AND m.id <= anchor.id)) AND EXISTS "+
				"(SELECT 1 FROM message_artifacts a WHERE a.session_id = s.id AND a.message_id = anchor.id "+
				"AND a.position = ? AND a.deleted_at IS NULL)",
				tenant, session, []string{"user", "assistant"}, true, user, position).
			Count(&count).Error
		if err != nil {
			return nil, 0, false, err
		}
		if count == 0 {
			return nil, 0, false, gorm.ErrRecordNotFound
		}
		page = int((count-1)/50) + 1
		if page > 10000 {
			return nil, 0, false, gorm.ErrRecordNotFound
		}
	}
	var rows []types.ChannelConversationMessage
	err := r.db.WithContext(ctx).Table("messages m").
		Select("m.id, m.role, m.content, m.created_at").
		Joins("JOIN sessions s ON s.id = m.session_id AND s.deleted_at IS NULL").
		Where("s.tenant_id = ? AND s.id = ? AND m.deleted_at IS NULL AND m.role IN ? AND "+channelArtifactGrant+
			" AND "+unambiguousChannelSession+
			" AND EXISTS (SELECT 1 FROM message_artifacts a JOIN messages am ON am.id = a.message_id "+
			"AND am.session_id = a.session_id AND am.deleted_at IS NULL WHERE a.session_id = s.id "+
			"AND a.message_id = ? AND a.position = ? AND a.deleted_at IS NULL)",
			tenant, session, []string{"user", "assistant"}, true, user, anchor, position).
		Order("m.created_at ASC, m.id ASC").Limit(51).Offset((page - 1) * 50).Scan(&rows).Error
	if err != nil {
		return nil, 0, false, err
	}
	hasMore := len(rows) > 50
	if hasMore {
		rows = rows[:50]
	}
	return rows, page, hasMore, nil
}
