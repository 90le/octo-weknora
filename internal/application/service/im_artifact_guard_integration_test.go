package service

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Exercise the collector through the real session manager and its guarded
// wrapper; only the Docker Engine and file storage are synthetic.
func TestIMArtifactCollectorUsesGuardedDockerSession(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	t.Setenv(sandbox.IMTrustedImageEnv, image)
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	t.Setenv("WEKNORA_SKILL_OUTPUT_DIR", "")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	daemon, host, certs := startIMQADocker(t, image)
	tenantConfig := &types.TenantSandboxConfig{
		SandboxType: "docker", AllowPrivateEndpoints: true,
		Docker: &types.DockerSandboxConfig{Image: "base", Host: host, TLSCertPath: certs},
	}
	db := newPinTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.TenantSandboxConfigEntity{}))
	require.NoError(t, db.Create(&types.TenantSandboxConfigEntity{
		ID: "cfg", TenantID: 7, Name: "test", SandboxType: "docker", Config: tenantConfig,
	}).Error)
	pinner := NewSessionSandboxPinner(db)
	resolver, err := sandbox.NewTenantSandboxResolver(sandbox.TenantSandboxResolverDeps{
		GlobalConfig: sandbox.DefaultConfig(), Loader: imQAConfigLoader{tenantConfig},
		Store: sandbox.NewMemorySessionSandboxBindingStore(), Checker: imQAChecker{},
	})
	require.NoError(t, err)
	var revoked atomic.Bool
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "s-1")
	ctx = types.WithIMKnowledgeScope(ctx, []string{"kb"})
	ctx = types.WithIMSandboxGrant(ctx, types.IMSandboxGrant{
		SessionID: "s-1", TenantID: 7, ConfigID: "cfg", AgentConfigDigest: strings.Repeat("a", 64), AllowShell: true,
		Revalidate: func(context.Context) error {
			if revoked.Load() {
				return types.ErrIMSandboxDenied
			}
			return nil
		},
	})
	sessionSvc := &sessionService{sandboxPinner: pinner, sandboxResolver: resolver}
	ctx, err = sessionSvc.PrepareIMSandboxTurn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, types.SealIMSandboxTurn(context.WithoutCancel(ctx))) })
	files := &fakeFileService{}
	collector := NewArtifactCollector(nil, files, &fakeStore{}, nil, ArtifactCollectorConfig{})
	collector.pinner, collector.resolver = pinner, resolver
	snapshot, err := collector.SnapshotIM(ctx, "s-1", "cfg", 7, 7, nil)
	require.NoError(t, err)

	manager, _, err := resolveIMSandboxForExecution(ctx, resolver, nil, pinner, 7, "s-1", "cfg", nil)
	require.NoError(t, err)
	guarded, ok := manager.(*imGuardedSandbox)
	require.True(t, ok)
	_, err = guarded.ExecShellCommand(ctx, "s-1", "printf ready", "/workspace", 0, nil)
	require.NoError(t, err)
	daemon.mu.Lock()
	daemon.listingPayload = fmt.Sprintf(
		`[ {"name":"report.pdf","path":"/workspace/output/report.pdf","size":%d,"mtime":100} ]`, 512*1024,
	)
	daemon.mu.Unlock()

	_, ok = collector.sessionSource(ctx, "s-1").(*imGuardedSandbox)
	require.True(t, ok, "IM artifact source must retain the guard and inspected binding")
	artifacts, err := collector.CollectIM(ctx, snapshot, "message", func(relative string, _ bool) IMArtifactSelection {
		if relative == "report.pdf" {
			return IMArtifactCurrentTurn
		}
		return IMArtifactNotSelected
	})
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, "report.pdf", artifacts[0].FileName)
	require.Equal(t, []byte(strings.Repeat("x", 512*1024)), files.saved[artifacts[0].URL])
	require.True(t, daemon.boundedListing.Load(), "directory listing must use the bounded IM helper")
	require.True(t, daemon.nofollowSeen.Load(), "file read must use component-level no-follow")

	revoked.Store(true)
	_, err = collector.CollectIM(ctx, snapshot, "other-message", func(string, bool) IMArtifactSelection {
		return IMArtifactCurrentTurn
	})
	require.Error(t, err, "revoked authorization must not collect a file")
}
