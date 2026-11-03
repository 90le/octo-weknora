package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestScopedIMNeverResolvesPinnedOrHostSandbox(t *testing.T) {
	db := newPinTestDB(t)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(context.Background(), "s-1", SandboxPin{ConfigID: "old", TenantID: 7})
	require.NoError(t, err)
	resolver := &tenantRecordingResolver{mgr: &pinTestManager{typ: sandbox.SandboxTypeDocker}}
	ctx := types.WithIMKnowledgeScope(context.Background(), []string{"kb"})
	for _, sessionID := range []string{"s-1", "another-session"} {
		mgr, pin, err := resolveSandboxForExecution(ctx, resolver, nil, pinner, 7, sessionID, "selected", nil,
			withLiteHostSandbox(&pinTestManager{typ: sandbox.SandboxTypeHost}), withLiteDesktop(true))
		require.NoError(t, err)
		require.Equal(t, sandbox.SandboxTypeDisabled, mgr.GetType())
		require.True(t, pin.IsZero())
		require.Empty(t, resolver.lastConfig)
	}
	pinned, err := pinner.Read(context.Background(), "s-1")
	require.NoError(t, err)
	require.Equal(t, "old", pinned.ConfigID, "deny must not destroy a live pin")
	// Unscoped Web/WeCom execution keeps the existing behavior.
	mgr, _, err := resolveSandboxForExecution(context.Background(), resolver, nil, pinner, 7, "s-1", "selected", nil)
	require.NoError(t, err)
	require.Equal(t, sandbox.SandboxTypeDocker, mgr.GetType())
	require.Equal(t, "old", resolver.lastConfig)
}

func TestScopedIMDoesNotTouchGlobalSandboxTurnHolder(t *testing.T) {
	holder := &turnLeaseManager{}
	gate := NewSessionBusyGate()
	svc := &sessionService{sandboxMgr: holder, busyGate: gate}
	ctx := types.WithIMKnowledgeScope(context.Background(), []string{"kb"})
	release, err := svc.holdSandboxTurn(ctx, "s-1", "")
	require.NoError(t, err)
	require.Zero(t, holder.begins)
	_, err = gate.TryLockRewind("s-1")
	require.ErrorIs(t, err, ErrRewindSourceBusy)
	release()
	require.Zero(t, holder.ends)
}

func TestValidateIMSandboxPinRequiresLiveMatchingAuthorization(t *testing.T) {
	db := newPinTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.TenantSandboxConfigEntity{}))
	require.NoError(t, db.Create(&types.TenantSandboxConfigEntity{ID: "cfg", TenantID: 7, Name: "test", SandboxType: "docker", Config: &types.TenantSandboxConfig{SandboxType: "docker"}}).Error)
	pinner := NewSessionSandboxPinner(db)
	allowed := true
	grant := types.IMSandboxGrant{SessionID: "s-1", TenantID: 7, ConfigID: "cfg", Revalidate: func(context.Context) error {
		if !allowed {
			return types.ErrIMSandboxDenied
		}
		return nil
	}}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "s-1")
	ctx = types.WithIMKnowledgeScope(ctx, []string{"kb"})
	ctx = types.WithIMSandboxGrant(ctx, grant)
	_, err := ValidateIMSandboxPin(ctx, pinner, "s-1", false)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	_, err = ValidateIMSandboxPin(ctx, pinner, "s-1", true)
	require.NoError(t, err)
	pin, err := pinner.Read(ctx, "s-1")
	require.NoError(t, err)
	require.True(t, pin.IsZero(), "validation is read-only")
	_, err = pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "other", TenantID: 7})
	require.NoError(t, err)
	_, err = ValidateIMSandboxPin(ctx, pinner, "s-1", true)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	require.NoError(t, pinner.Clear(ctx, "s-1"))
	_, err = pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg", TenantID: 7})
	require.NoError(t, err)
	output, err := IMSandboxOutputDir(ctx, pinner, "s-1")
	require.NoError(t, err)
	require.Equal(t, sandbox.RemoteWorkspaceLayout().OutputDir, output)
	_, err = ValidateIMSandboxPin(ctx, pinner, "other-session", false)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	_, err = ValidateIMSandboxPin(types.WithSessionID(ctx, "other-session"), pinner, "s-1", false)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	allowed = false
	_, err = ValidateIMSandboxPin(ctx, pinner, "s-1", false)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	allowed = true
	policy := &types.TenantSandboxConfigEntity{ID: "kill", TenantID: 7, Name: types.SandboxWorkspacePolicyConfigName, SandboxType: "disabled", Config: &types.TenantSandboxConfig{SandboxType: "disabled"}}
	require.NoError(t, db.Create(policy).Error)
	_, err = ValidateIMSandboxPin(ctx, pinner, "s-1", false)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	require.NoError(t, db.Delete(policy).Error)
	require.NoError(t, db.Model(&types.Session{}).Where("id = ?", "s-1").Update("sandbox_config_tenant_id", 99).Error)
	_, err = ValidateIMSandboxPin(ctx, pinner, "s-1", false)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	require.NoError(t, db.Model(&types.Session{}).Where("id = ?", "s-1").Update("sandbox_config_tenant_id", 7).Error)
	require.NoError(t, db.Model(&types.TenantSandboxConfigEntity{}).Where("id = ?", "cfg").Update("sandbox_type", "host").Error)
	_, err = ValidateIMSandboxPin(ctx, pinner, "s-1", false)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	require.NoError(t, db.Model(&types.TenantSandboxConfigEntity{}).Where("id = ?", "cfg").Update("sandbox_type", "docker").Error)
	require.NoError(t, db.Model(&types.Session{}).Where("id = ?", "s-1").Update("sandbox_config_tenant_id", -1).Error)
	_, err = ValidateIMSandboxPin(ctx, pinner, "s-1", true)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	require.NoError(t, db.Model(&types.Session{}).Where("id = ?", "s-1").Updates(map[string]any{"sandbox_config_tenant_id": 0, "sandbox_config_id": "  "}).Error)
	_, err = ValidateIMSandboxPin(ctx, pinner, "s-1", true)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	missingGrant := grant
	missingGrant.SessionID = "missing"
	missingCtx := types.WithIMSandboxGrant(types.WithSessionID(ctx, "missing"), missingGrant)
	_, err = ValidateIMSandboxPin(missingCtx, pinner, "missing", true)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	_, err = ValidateIMSandboxPin(ctx, pinner, "s-1", true)
	require.ErrorIs(t, err, types.ErrIMSandboxDenied)
}
