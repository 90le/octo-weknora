package service

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

// ValidateIMSandboxPin checks the trusted context, live authorization and the
// session's actual pin. It never creates a pin or sandbox. allowCreate permits
// an absent pin only before creation: the caller must validate again after
// claiming the pin, and reject a concurrent winner naming another config.
// For artifact collection always pass allowCreate=false.
func ValidateIMSandboxPin(
	ctx context.Context,
	pinner *SessionSandboxPinner,
	sessionID string,
	allowCreate bool,
) (types.IMSandboxGrant, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok ||
		!types.HasIMKnowledgeScope(ctx) ||
		pinner == nil ||
		pinner.db == nil ||
		sessionID == "" ||
		sessionID != grant.SessionID ||
		strings.TrimSpace(grant.ConfigID) != grant.ConfigID ||
		grant.ConfigID == types.SandboxConfigIDGlobalDefault ||
		sandbox.IsHostSkillTarget(grant.ConfigID) {
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	if sid, ok := types.SessionIDFromContext(ctx); !ok || sid != grant.SessionID {
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	if tenant, ok := types.TenantIDFromContext(ctx); !ok || tenant != grant.TenantID {
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	if err := grant.Revalidate(ctx); err != nil {
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	// A workspace-wide script kill switch also revokes this sandbox. Unlike
	// the legacy resolver, a policy lookup error here must fail closed.
	var disabled int64
	if err := pinner.db.WithContext(ctx).Model(&types.TenantSandboxConfigEntity{}).Where(
		"tenant_id = ? AND name = ?", grant.TenantID, types.SandboxWorkspacePolicyConfigName,
	).Count(&disabled).Error; err != nil || disabled != 0 {
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	cfg, err := repository.NewTenantSandboxConfigRepository(pinner.db).GetByID(ctx, grant.TenantID, grant.ConfigID)
	if err != nil ||
		cfg == nil ||
		cfg.Config == nil ||
		cfg.Config.SandboxType != cfg.SandboxType ||
		!sandbox.IsNamedSandboxBackendType(cfg.SandboxType) ||
		cfg.IsCordoned(time.Now(), types.SandboxCordonLease) {
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	// Read the session row strictly: unlike the legacy pin reader, a negative
	// tenant or malformed whitespace is not interpreted as an absent owner.
	var row struct {
		SandboxConfigID       sql.NullString
		SandboxConfigTenantID sql.NullInt64
	}
	result := pinner.db.WithContext(ctx).Model(&types.Session{}).
		Where("id = ? AND tenant_id = ?", sessionID, grant.TenantID).
		Select("sandbox_config_id", "sandbox_config_tenant_id").Scan(&row)
	if result.Error != nil || result.RowsAffected != 1 ||
		(row.SandboxConfigTenantID.Valid && row.SandboxConfigTenantID.Int64 < 0) {
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	configID := row.SandboxConfigID.String
	if configID != strings.TrimSpace(configID) {
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	if configID == "" {
		if allowCreate && (!row.SandboxConfigTenantID.Valid || row.SandboxConfigTenantID.Int64 == 0) {
			return grant, nil
		}
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	if configID != grant.ConfigID ||
		(row.SandboxConfigTenantID.Valid &&
			row.SandboxConfigTenantID.Int64 != 0 &&
			uint64(row.SandboxConfigTenantID.Int64) != grant.TenantID) {
		return types.IMSandboxGrant{}, types.ErrIMSandboxDenied
	}
	return grant, nil
}

// IMSandboxOutputDir provides only the remote output root. The caller must
// still enforce canonical paths and reject symlink escapes when reading files.
func IMSandboxOutputDir(ctx context.Context, pinner *SessionSandboxPinner, sessionID string) (string, error) {
	if _, err := ValidateIMSandboxPin(ctx, pinner, sessionID, false); err != nil {
		return "", err
	}
	return sandbox.RemoteWorkspaceLayout().OutputDir, nil
}
