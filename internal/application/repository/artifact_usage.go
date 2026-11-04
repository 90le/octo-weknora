package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ArtifactUsage counts visible artifact version records. Shared references can
// be counted more than once, so this is not a physical disk measurement.
func (r *messageRepository) ArtifactUsage(ctx context.Context, q *types.ArtifactLibraryQuery) (int64, int64, error) {
	webScope := "(s.description IS NULL OR s.description NOT LIKE ?) AND " + webSessionPredicate
	args := []any{q.TenantID, types.SkillMaintenanceSessionMarker + "%"}
	args = append(args, webSessionPredicateArgs()...)
	if q.UserID != "" {
		webScope = "(s.user_id = ? OR s.user_id IS NULL OR s.user_id = '') AND " + webScope
		args = append([]any{q.TenantID, q.UserID}, args[1:]...)
	}
	scope := webScope
	if q.WebOwnerID != "" && r.db.WithContext(ctx).Migrator().HasTable("im_channel_web_owners") {
		scope = "((" + webScope + ") OR " + joinedChannelArtifactGrant + ")"
		args = append(args, true, q.WebOwnerID)
	}
	query := `SELECT COUNT(*) AS file_count,
		COALESCE(SUM(CASE WHEN file_size > 0 THEN file_size ELSE 0 END), 0) AS logical_bytes FROM (
		SELECT DISTINCT ma.id, ma.file_size
		FROM message_artifacts ma
		JOIN messages m ON m.id = ma.message_id AND m.session_id = ma.session_id AND m.deleted_at IS NULL
		JOIN sessions s ON s.id = ma.session_id
		LEFT JOIN im_channel_sessions ics ON ics.session_id = s.id
		WHERE s.tenant_id = ? AND s.deleted_at IS NULL AND ma.deleted_at IS NULL
		AND ma.url IS NOT NULL AND ma.url <> '' AND (` + scope + `)
	) visible_versions`
	var result struct{ FileCount, LogicalBytes int64 }
	err := r.db.WithContext(ctx).Raw(query, args...).Scan(&result).Error
	return result.FileCount, result.LogicalBytes, err
}
