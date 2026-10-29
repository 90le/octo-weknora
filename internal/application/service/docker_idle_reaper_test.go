package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type idleReaperTestManager struct {
	sandbox.Manager
	calls int
}

func (m *idleReaperTestManager) ReapIdleDocker(context.Context) (int, error) {
	m.calls++
	return 0, nil
}

type idleReaperTestResolver struct {
	manager *idleReaperTestManager
	ids     []string
}

func (r *idleReaperTestResolver) Resolve(_ context.Context, _ uint64, id string) (sandbox.Manager, error) {
	r.ids = append(r.ids, id)
	return r.manager, nil
}

func TestDockerIdleReaperOnlyScansSavedUncordonedDockerConfigs(t *testing.T) {
	manager := &idleReaperTestManager{}
	resolver := &idleReaperTestResolver{manager: manager}
	row := &types.TenantSandboxConfigEntity{ID: "cfg", TenantID: 7, SandboxType: "docker"}
	store := &reaperConfigStore{entity: row}
	svc := &TenantSkillService{configs: store, sandboxes: resolver}
	svc.runDockerIdleReaper(context.Background())
	require.Equal(t, []string{"cfg"}, resolver.ids)
	require.Equal(t, 1, manager.calls)

	now := time.Now()
	row.CordonedAt = &now
	svc.runDockerIdleReaper(context.Background())
	row.CordonedAt = nil
	row.SandboxType = "cube"
	svc.runDockerIdleReaper(context.Background())
	require.Equal(t, 1, manager.calls)
}
