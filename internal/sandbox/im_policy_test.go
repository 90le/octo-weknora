package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	dockerimage "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

type imTestDiskVerifier struct{}

func (imTestDiskVerifier) VerifyIMSandboxDisk(context.Context, uint64, string, string, container.InspectResponse) error {
	return nil
}

func TestIMPolicyLimitsAndIsolation(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	t.Setenv(IMTrustedImageEnv, imageID)
	grant := types.IMSandboxGrant{SessionID: "session", TenantID: 7, ConfigID: "cfg", AllowShell: true, Revalidate: func(context.Context) error { return nil }}
	ctx := types.WithIMSandboxGrant(types.WithIMKnowledgeScope(context.Background(), nil), grant)
	tenant := &types.TenantSandboxConfig{SandboxType: "docker", Docker: &types.DockerSandboxConfig{Image: "shared"}}
	cfg := &Config{Type: SandboxTypeDocker, DockerImage: "shared", TemplateFromSnapshot: true, DockerNetworkMode: "bridge", EnvVars: map[string]string{"SECRET": "bad"}, DockerCPULimit: 5, DockerMemoryBytes: 2 << 30, DockerPidsLimit: 999, DefaultTimeout: time.Hour}
	require.NoError(t, restrictIMSandboxConfig(ctx, "cfg", tenant, cfg))
	require.Equal(t, imageID, cfg.DockerImage)
	require.Empty(t, cfg.EnvVars)
	require.False(t, cfg.TemplateFromSnapshot)
	require.Equal(t, "none", cfg.DockerNetworkMode)
	require.False(t, *cfg.Network.AllowInternetAccess)
	require.False(t, *cfg.Network.AllowPublicTraffic)
	require.Equal(t, IMMaxCPU, cfg.DockerCPULimit)
	require.Equal(t, IMMaxMemoryBytes, cfg.DockerMemoryBytes)
	require.Equal(t, IMMaxPIDs, cfg.DockerPidsLimit)
	require.Equal(t, IMMaxCommandTime, cfg.DefaultTimeout)
	tenant.VolumeMount = &types.VolumeMountConfig{}
	require.Error(t, restrictIMSandboxConfig(ctx, "cfg", tenant, cfg))
	tenant.VolumeMount = nil
	tenant.SandboxType = "e2b"
	require.Error(t, restrictIMSandboxConfig(ctx, "cfg", tenant, cfg))
	tenant.SandboxType = "docker"
	require.Error(t, restrictIMSandboxConfig(ctx, "other", tenant, cfg))
	t.Setenv(IMTrustedImageEnv, "latest")
	require.Error(t, restrictIMSandboxConfig(ctx, "cfg", tenant, cfg))
	untouched := &Config{Type: SandboxTypeDocker, DockerImage: "web", EnvVars: map[string]string{"S": "v"}}
	require.NoError(t, restrictIMSandboxConfig(context.Background(), "cfg", tenant, untouched))
	require.Equal(t, "web", untouched.DockerImage)
	require.Equal(t, "v", untouched.EnvVars["S"])
}

func TestIMInspectRejectsUnsafeContainerAndRevokedPolicy(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	policy := strings.Repeat("b", 64)
	pids := int64(128)
	key := SessionSandboxKey{TenantID: 7, SessionID: "session"}
	binding := SessionSandboxBinding{SandboxID: "container", ConfigID: "cfg"}
	img := image.InspectResponse{ID: id, Config: &dockerimage.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Env: []string{"PATH=/bin"}}}}
	labels := map[string]string{dockerManagedLabel: "true", remoteMetadataTenantID: "7", remoteMetadataSessionID: "session", remoteMetadataConfigID: "cfg", remoteMetadataProvider: string(SandboxTypeDocker), remoteMetadataBindingVersion: "1", remoteMetadataIMPolicy: policy}
	c := container.InspectResponse{ID: "container", Image: id, Config: &container.Config{Labels: labels, Env: []string{"PATH=/bin", skillOutputEnvVar + "=" + SessionOutputRoot, sessionInputEnvVar + "=" + SessionInputRoot}}, HostConfig: &container.HostConfig{NetworkMode: "none", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}}}
	c.HostConfig.Memory = IMMaxMemoryBytes
	c.HostConfig.MemorySwap = IMMaxMemoryBytes
	c.HostConfig.NanoCPUs = int64(IMMaxCPU * 1e9)
	c.HostConfig.PidsLimit = &pids
	c.HostConfig.ReadonlyRootfs = true
	c.HostConfig.Tmpfs = map[string]string{SessionWorkspaceRoot: imWorkspaceTmpfs, "/tmp": imTmpTmpfs}
	c.HostConfig.ShmSize = 64 << 20
	require.NoError(t, verifyIMDockerContainer(c, img, binding, key, id, policy))
	c.HostConfig.ReadonlyRootfs = false
	require.Error(t, verifyIMDockerContainer(c, img, binding, key, id, policy))
	c.HostConfig.ReadonlyRootfs = true
	c.HostConfig.Tmpfs[SessionWorkspaceRoot] = "rw,size=999999999"
	require.Error(t, verifyIMDockerContainer(c, img, binding, key, id, policy))
	c.HostConfig.Tmpfs[SessionWorkspaceRoot] = imWorkspaceTmpfs
	c.HostConfig.CapAdd = []string{"CAP_CHOWN"}
	require.Error(t, verifyIMDockerContainer(c, img, binding, key, id, policy))
	c.HostConfig.CapAdd = nil
	require.Error(t, verifyIMDockerContainer(c, img, binding, key, id, strings.Repeat("c", 64)))
	c.HostConfig.NetworkMode = "bridge"
	require.Error(t, verifyIMDockerContainer(c, img, binding, key, id, policy))
	c.HostConfig.NetworkMode = "none"
	c.HostConfig.Binds = []string{"/var/run/docker.sock:/var/run/docker.sock"}
	require.Error(t, verifyIMDockerContainer(c, img, binding, key, id, policy))
	c.HostConfig.Binds = nil
	c.Config.Env = append(c.Config.Env, "TOKEN=secret")
	require.Error(t, verifyIMDockerContainer(c, img, binding, key, id, policy))
	c.Config.Env = c.Config.Env[:3]
	c.HostConfig.PidsLimit = nil
	require.Error(t, verifyIMDockerContainer(c, img, binding, key, id, policy))
	c.HostConfig.PidsLimit = &pids
	img.Config.Volumes = map[string]struct{}{"/secret": {}}
	require.Error(t, verifyIMDockerContainer(c, img, binding, key, id, policy))
}

func TestIMOutputBudgetRejectsExcess(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 101)
	r := &imBudgetReader{inner: bytes.NewReader(data), remaining: 100}
	got, err := io.ReadAll(r)
	require.Equal(t, 100, len(got))
	require.ErrorContains(t, err, "exceeded")
	got, err = io.ReadAll(&imBudgetReader{inner: bytes.NewReader(data[:100]), remaining: 100})
	require.NoError(t, err)
	require.Len(t, got, 100)
	_, err = WithIMDockerOutputLimit(context.Background(), 0)
	require.Error(t, err)
	_, err = WithIMDockerOutputLimit(context.Background(), 50<<20)
	require.NoError(t, err)
	fake := newFakeDockerEngine()
	_, err = (&imLimitedDockerEngine{dockerEngineAPI: fake}).ImagePull(context.Background(), "missing", client.ImagePullOptions{})
	require.Error(t, err)
	require.Empty(t, fake.pulled)
	type absent struct{}
	require.ErrorIs(t, setIMOutputLimit(&absent{}, 4), ErrIMOutputLimitUnavailable)
	type present struct{ MaxOutputBytes int64 }
	p := present{}
	require.NoError(t, setIMOutputLimit(&p, 123))
	require.Equal(t, int64(123), p.MaxOutputBytes)
}

// The parent integration supplies MaxOutputBytes; the independent baseline
// deliberately skips this exercised path until that transport is present.
func TestIMDockerManagerExecutesOnlyInspectedSession(t *testing.T) {
	if err := RequireIMBoundedOutput(); err != nil {
		t.Skip("bounded transport is supplied by the artifact integration")
	}
	imageID := "sha256:" + strings.Repeat("a", 64)
	policy := strings.Repeat("b", 64)
	t.Setenv(IMTrustedImageEnv, imageID)
	fake := newFakeDockerEngine()
	engine := &imFixtureEngine{fakeDockerEngine: fake, imageID: imageID}
	settings, err := dockerSettingsFromConfig(&Config{Type: SandboxTypeDocker, DockerImage: imageID, DockerNetworkMode: "none", DockerCPULimit: IMMaxCPU, DockerMemoryBytes: IMMaxMemoryBytes, DockerPidsLimit: IMMaxPIDs})
	require.NoError(t, err)
	settings.IdleTTL = 0
	docker := newDockerRemoteClientWithAPI(&imLimitedDockerEngine{dockerEngineAPI: engine}, settings)
	cfg := &Config{Type: SandboxTypeDocker, DockerImage: imageID, DockerNetworkMode: "none", DockerCPULimit: IMMaxCPU, DockerMemoryBytes: IMMaxMemoryBytes, DockerPidsLimit: IMMaxPIDs, DefaultTimeout: IMMaxCommandTime}
	mgr, err := NewSessionBoundManager(SessionBoundManagerConfig{Config: cfg, Client: docker, Store: NewMemorySessionSandboxBindingStore(), Checker: &fakeSessionExistenceChecker{exists: true}, ConfigID: "cfg", SkipHealthProbe: true, IMDiskVerifier: imTestDiskVerifier{}})
	require.NoError(t, err)
	mgr.lifecycle.createRequest.Metadata = map[string]string{remoteMetadataIMPolicy: policy}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithIMKnowledgeScope(ctx, []string{"kb"})
	ctx = types.WithSessionID(ctx, "session")
	ctx = types.WithIMSandboxPolicy(ctx, policy)
	allowed := true
	ctx = types.WithIMSandboxGrant(ctx, types.IMSandboxGrant{SessionID: "session", TenantID: 7, ConfigID: "cfg", AllowShell: true, Revalidate: func(context.Context) error {
		if !allowed {
			return types.ErrIMSandboxDenied
		}
		return nil
	}})
	// Production currently wires no disk verifier, so this must fail before
	// creating or binding any container; the following verifier is a fake.
	denied := *mgr
	denied.imDiskVerifier = nil
	_, err = denied.EnsureIMSession(ctx, "session", true)
	require.ErrorIs(t, err, ErrIMDiskLimitUnavailable)
	require.Empty(t, fake.created)
	id, err := mgr.EnsureIMSession(ctx, "session", true)
	require.NoError(t, err)
	require.Equal(t, "container-1", id)
	fake.execStdout = "ok"
	safe := WithIMExpectedSandbox(ctx, "session", id)
	streamed := 0
	safe = WithCommandOutput(safe, func(_ string, data []byte) { streamed += len(data) })
	result, err := mgr.ExecIMShell(safe, "session", id, "printf hello", SessionWorkspaceRoot, time.Minute, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", result.Stdout)
	require.Zero(t, streamed, "IM must not stream before final revalidation")
	require.Contains(t, fake.stopped, id, "completed commands must stop their container")
	require.Len(t, fake.created, 1)
	require.Equal(t, "none", string(fake.created[0].HostConfig.NetworkMode))
	require.Empty(t, fake.created[0].HostConfig.Binds)
	require.Empty(t, fake.created[0].HostConfig.CapAdd)
	require.True(t, fake.created[0].HostConfig.ReadonlyRootfs)
	require.Equal(t, imWorkspaceTmpfs, fake.created[0].HostConfig.Tmpfs[SessionWorkspaceRoot])
	require.Equal(t, imTmpTmpfs, fake.created[0].HostConfig.Tmpfs["/tmp"])
	_, err = mgr.EnsureIMSession(ctx, "other", true)
	require.Error(t, err)
	allowed = false
	_, err = mgr.EnsureIMSession(ctx, "session", true)
	require.Error(t, err)
	allowed = true
	// Reauthorization under exactly the same policy preserves the existing
	// container and files, even if the ordinary lifecycle marked it stale.
	_, err = mgr.EnsureIMSession(ctx, "session", false)
	require.NoError(t, err)
	_, err = mgr.bindings.InvalidateByConfig(ctx, 7, "cfg")
	require.NoError(t, err)
	resumed, err := mgr.EnsureIMSession(ctx, "session", false)
	require.NoError(t, err)
	require.Equal(t, id, resumed)
	require.Len(t, fake.created, 1)
	require.Empty(t, fake.removed)
	changedPolicy := strings.Repeat("c", 64)
	changed := types.WithIMSandboxPolicy(ctx, changedPolicy)
	// A new resolver carries the new policy while the old container still
	// has the previous immutable label.
	mgr.lifecycle.createRequest.Metadata[remoteMetadataIMPolicy] = changedPolicy
	_, err = mgr.EnsureIMSession(changed, "session", true)
	require.Error(t, err)
	require.Len(t, fake.created, 1)
	require.Empty(t, fake.removed, "changed policy must preserve the old container")
	mgr.lifecycle.createRequest.Metadata[remoteMetadataIMPolicy] = policy
	resumed, err = mgr.EnsureIMSession(ctx, "session", false)
	require.NoError(t, err)
	require.Equal(t, id, resumed)
	// A wedged Docker stream is stopped from outside the container, even if
	// an attacker changed the in-container timeout executable.
	priorStops := len(fake.stopped)
	fake.execStreamStalls = true
	_, err = mgr.ExecIMShell(safe, "session", id, "sleep 100", SessionWorkspaceRoot, 20*time.Millisecond, nil)
	require.Error(t, err)
	require.Greater(t, len(fake.stopped), priorStops, "timeout must request another stop")
	require.Contains(t, fake.stopped, id)
	fake.execStreamStalls = false
	fake.inspect[id] = func() container.InspectResponse { v := fake.inspect[id]; v.HostConfig.NetworkMode = "bridge"; return v }()
	_, err = mgr.EnsureIMSession(ctx, "session", true)
	require.Error(t, err)
}

type imFixtureEngine struct {
	*fakeDockerEngine
	imageID string
}

func (f *imFixtureEngine) ImageInspect(_ context.Context, id string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	if id != f.imageID {
		return client.ImageInspectResult{}, errors.New("unknown image")
	}
	return client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: id, Config: &dockerimage.DockerOCIImageConfig{}}}, nil
}
func (f *imFixtureEngine) ContainerStop(ctx context.Context, id string, options client.ContainerStopOptions) (client.ContainerStopResult, error) {
	result, err := f.fakeDockerEngine.ContainerStop(ctx, id, options)
	if err == nil {
		v := f.inspect[id]
		v.State.Status = "exited"
		v.State.Running = false
		f.inspect[id] = v
	}
	return result, err
}
func (f *imFixtureEngine) ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	result, err := f.fakeDockerEngine.ContainerCreate(ctx, options)
	if err != nil {
		return result, err
	}
	f.inspect[result.ID] = container.InspectResponse{ID: result.ID, Image: f.imageID, Config: options.Config, HostConfig: options.HostConfig, State: &container.State{Status: "created"}}
	return result, nil
}

func TestIMDockerOutputLimitBeforeLiveCallback(t *testing.T) {
	if err := RequireIMBoundedOutput(); err != nil {
		t.Skip("bounded transport is supplied by the artifact integration")
	}
	fake := newFakeDockerEngine()
	fake.inspect["c"] = container.InspectResponse{ID: "c", State: &container.State{Status: "running"}}
	docker := newTestDockerClient(t, fake)
	fake.execStdout = strings.Repeat("x", 512)
	var delivered int
	req := RemoteExecRequest{Command: "printf", Timeout: time.Second, OnOutput: func(_ string, b []byte) { delivered += len(b) }}
	require.NoError(t, setIMOutputLimit(&req, 128))
	_, err := docker.Exec(context.Background(), testHandle("c"), req)
	require.Error(t, err)
	require.LessOrEqual(t, delivered, 128)
	// The existing Web/WeCom request has no limit and retains its behavior.
	req = RemoteExecRequest{Command: "printf", Timeout: time.Second}
	result, err := docker.Exec(context.Background(), testHandle("c"), req)
	require.NoError(t, err)
	require.Len(t, result.Stdout, 512)
}
