package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// ReadChannelChat keeps authorization in each query, including the message
// query, so the normal session ownership checks are never bypassed elsewhere.
func (r *messageRepository) ReadChannelChat(
	ctx context.Context, tenant uint64, user, id string, limit int, before *time.Time, beforeID string,
) (*types.Session, []*types.Message, error) {
	if tenant == 0 || user == "" || id == "" || limit < 1 || limit > 100 {
		return nil, nil, gorm.ErrRecordNotFound
	}
	grant := "s.tenant_id = ? AND s.id = ? AND s.deleted_at IS NULL AND " + channelArtifactGrant +
		" AND " + unambiguousChannelSession +
		" AND EXISTS (SELECT 1 FROM im_channel_sessions x JOIN im_channels c ON c.id = x.im_channel_id " +
		"AND c.tenant_id = x.tenant_id WHERE x.session_id = s.id AND c.platform = 'octo')"
	args := []any{tenant, id, true, user}
	var session types.Session
	result := r.db.WithContext(ctx).Table("sessions s").
		Select("s.id, s.title, s.tenant_id, s.created_at, s.updated_at").Where(grant, args...).Take(&session)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	session.IMPlatform = "octo"
	messages := make([]*types.Message, 0)
	query := r.db.WithContext(ctx).Table("messages m").
		Select("m.id, m.session_id, m.request_id, m.content, m.role, m.is_completed, m.is_fallback, "+
			"m.channel, m.created_at, m.updated_at, m.knowledge_references").
		Joins("JOIN sessions s ON s.id = m.session_id").
		Where(grant+" AND m.deleted_at IS NULL AND m.role IN ?", append(args, []string{"user", "assistant"})...)
	if before != nil {
		if beforeID != "" {
			query = query.Where("(m.created_at < ? OR (m.created_at = ? AND m.id < ?))", *before, *before, beforeID)
		} else {
			query = query.Where("m.created_at < ?", *before)
		}
	}
	err := query.Order("m.created_at DESC, m.id DESC").Limit(limit).Scan(&messages).Error
	if err != nil {
		return nil, nil, err
	}
	if len(messages) > 0 {
		ids := make([]string, 0, len(messages))
		byID := make(map[string]*types.Message, len(messages))
		for _, message := range messages {
			ids = append(ids, message.ID)
			byID[message.ID] = message
		}
		var artifacts []types.MessageArtifactRecord
		err = r.db.WithContext(ctx).Table("message_artifacts a").
			Select("a.message_id, a.position, a.url, a.file_name, a.file_type, "+
				"a.file_size, a.created_at, a.deleted_at").
			Joins("JOIN messages m ON m.id = a.message_id AND m.session_id = a.session_id AND m.deleted_at IS NULL").
			Joins("JOIN sessions s ON s.id = a.session_id").
			Where(grant+" AND a.message_id IN ? AND m.role = ?", append(args, ids, "assistant")...).
			Order("a.message_id, a.position").Scan(&artifacts).Error
		if err != nil {
			return nil, nil, err
		}
		for _, artifact := range artifacts {
			if artifact.Position < 0 || artifact.Position > 10000 {
				return nil, nil, gorm.ErrInvalidData
			}
			message := byID[artifact.MessageID]
			for len(message.Artifacts) <= artifact.Position {
				// Preserve positions, including deleted/missing records, without
				// turning a gap into a link to a different artifact.
				deleted := time.Time{}
				message.Artifacts = append(message.Artifacts, types.MessageArtifact{DeletedAt: &deleted})
			}
			message.Artifacts[artifact.Position] = types.MessageArtifact{
				FileName: artifact.FileName, FileType: artifact.FileType, FileSize: artifact.FileSize,
				CreatedAt: artifact.CreatedAt, DeletedAt: artifact.DeletedAt,
			}
			if channelArtifactHandleInContent(artifact.URL, message.Content) {
				message.Artifacts[artifact.Position].URL = artifact.URL
			}
		}
	}
	// Recheck after reading messages as well: a revoked grant must not turn
	// into a successful response containing the previously read session.
	var count int64
	if err := r.db.WithContext(ctx).Table("sessions s").Where(grant, args...).Count(&count).Error; err != nil {
		return nil, nil, err
	}
	if count != 1 {
		return nil, nil, gorm.ErrRecordNotFound
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return &session, messages, nil
}

// Never expose a storage locator or a handle that only belongs to another
// message. The handler repeats this check after removing assistant thoughts.
func channelArtifactHandleInContent(ref, content string) bool {
	handle, ok := types.ParseResourcePath(ref)
	if !ok || ref != types.BuildResourcePath(handle) {
		return false
	}
	for _, candidate := range types.ScanResourceReferences(content) {
		if candidate == ref {
			return true
		}
	}
	return false
}
