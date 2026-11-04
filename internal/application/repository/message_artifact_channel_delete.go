package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeleteChannelArtifact holds the authorization rows through the tombstone commit.
// It never grants access to the session itself. Cleared mappings remain eligible,
// while identity-retired mappings have had their channel association removed.
func (r *messageRepository) DeleteChannelArtifact(
	ctx context.Context, tenant uint64, user, tokenID string, expiry time.Time, req *types.ArtifactDeleteRequest,
) ([]types.MessageArtifactRecord, []types.ArtifactRef, error) {
	var rows []types.MessageArtifactRecord
	var marked []types.ArtifactRef
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var mappings []struct {
			ID          string
			IMChannelID string
		}
		if err := tx.Table("im_channel_sessions").Select("id, im_channel_id").
			Where("session_id = ? AND tenant_id = ? AND platform = ?", req.SessionID, tenant, "octo").
			Find(&mappings).Error; err != nil {
			return err
		}
		if len(mappings) != 1 || mappings[0].IMChannelID == "" {
			return gorm.ErrRecordNotFound
		}
		channel := mappings[0].IMChannelID
		// Match SetWebOwner's member -> user -> channel lock order.
		for _, q := range []struct {
			table, where string
			args         []any
		}{
			{
				"tenant_members", "tenant_id = ? AND user_id = ? AND status = ? AND deleted_at IS NULL",
				[]any{tenant, user, "active"},
			},
			{"users", "id = ? AND is_active = ? AND deleted_at IS NULL", []any{user, true}},
			{
				"im_channels", "id = ? AND tenant_id = ? AND platform = ? AND deleted_at IS NULL",
				[]any{channel, tenant, "octo"},
			},
			{
				"im_channel_web_owners",
				"tenant_id = ? AND im_channel_id = ? AND web_user_id = ? AND deleted_at IS NULL",
				[]any{tenant, channel, user},
			},
			{
				"im_channel_sessions",
				"id = ? AND tenant_id = ? AND session_id = ? AND im_channel_id = ? AND platform = ?",
				[]any{mappings[0].ID, tenant, req.SessionID, channel, "octo"},
			},
			{"sessions", "id = ? AND tenant_id = ? AND deleted_at IS NULL", []any{req.SessionID, tenant}},
			{"messages", "id = ? AND session_id = ? AND deleted_at IS NULL", []any{req.MessageID, req.SessionID}},
		} {
			var row struct{ ID string }
			if err := tx.Table(q.table).Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
				Where(q.where, q.args...).Take(&row).Error; err != nil {
				return err
			}
		}
		var token types.AuthToken
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND token_type = ? AND is_revoked = ?", tokenID, user, "access_token", false).
			Take(&token).Error; err != nil {
			return err
		}
		if expiry.IsZero() || !expiry.After(time.Now()) ||
			token.ExpiresAt.IsZero() || !token.ExpiresAt.After(time.Now()) {
			return gorm.ErrRecordNotFound
		}
		var target types.MessageArtifactRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("session_id = ? AND message_id = ? AND position = ? AND deleted_at IS NULL",
				req.SessionID, req.MessageID, req.Index).Take(&target).Error; err != nil {
			return err
		}
		rows = []types.MessageArtifactRecord{target}
		if req.AllVersions && target.SourcePath != "" {
			// Message updates replace artifact rows. Serialize with those writers
			// before reading the version set so stale snapshots cannot revive it.
			var messageIDs []string
			if err := tx.Table("message_artifacts AS ma").Distinct("ma.message_id").
				Joins("JOIN messages m ON m.id = ma.message_id AND m.session_id = ma.session_id "+
					"AND m.deleted_at IS NULL").
				Where("ma.session_id = ? AND ma.source_path = ? AND ma.deleted_at IS NULL",
					req.SessionID, target.SourcePath).
				Pluck("ma.message_id", &messageIDs).Error; err != nil {
				return err
			}
			for _, id := range messageIDs {
				var message struct{ ID string }
				if err := tx.Table("messages").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("id = ? AND session_id = ? AND deleted_at IS NULL", id, req.SessionID).
					Take(&message).Error; err != nil {
					return err
				}
			}
			if err := tx.Table("message_artifacts AS ma").Select("ma.*").
				Joins("JOIN messages m ON m.id = ma.message_id AND m.session_id = ma.session_id "+
					"AND m.deleted_at IS NULL").
				Where("ma.session_id = ? AND ma.source_path = ? AND ma.deleted_at IS NULL",
					req.SessionID, target.SourcePath).
				Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "ma"}}).
				Find(&rows).Error; err != nil {
				return err
			}
		}
		// Recheck wall-clock expiry immediately before claiming any rows.
		if !expiry.After(time.Now()) || !token.ExpiresAt.After(time.Now()) {
			return gorm.ErrRecordNotFound
		}
		for _, row := range rows {
			res := tx.Model(&types.MessageArtifactRecord{}).
				Where("id = ? AND deleted_at IS NULL", row.ID).Update("deleted_at", time.Now().UTC())
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 1 {
				marked = append(marked, types.ArtifactRef{MessageID: row.MessageID, Position: row.Position})
			}
		}
		if len(marked) == 0 {
			return gorm.ErrRecordNotFound
		}
		if !expiry.After(time.Now()) || !token.ExpiresAt.After(time.Now()) {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return rows, marked, nil
}
