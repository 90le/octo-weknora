package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type runtimeCountClient struct {
	*stubProviderClient
	filter sandbox.RemoteListFilter
}

func (c *runtimeCountClient) List(
	_ context.Context, filter sandbox.RemoteListFilter,
) ([]sandbox.RemoteSandboxSummary, error) {
	c.filter = filter
	return []sandbox.RemoteSandboxSummary{{State: sandbox.RemoteStateRunning}, {State: sandbox.RemoteStatePaused}}, nil
}

func TestRuntimeDockerCountFiltersTenantAndCountsRunning(t *testing.T) {
	repo := &fakeConfigRepo{entity: &types.TenantSandboxConfigEntity{
		ID: "cfg", TenantID: 7,
		Config: &types.TenantSandboxConfig{
			SandboxType: "docker", Docker: &types.DockerSandboxConfig{Image: "test"},
		},
	}}
	svc := newTestConfigService(t, repo, nil, stubAgentRepo{})
	client := &runtimeCountClient{stubProviderClient: &stubProviderClient{}}
	svc.newClient = func(*sandbox.Config) (sandbox.ConfigSandboxClient, error) { return client, nil }
	count, err := svc.RuntimeDockerCount(context.Background(), 7, "cfg")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, "7", client.filter.Metadata["weknora_tenant_id"])
	require.Equal(t, "cfg", client.filter.Metadata["weknora_sandbox_config_id"])
	require.Contains(t, client.filter.States, sandbox.RemoteStateRunning)
}
