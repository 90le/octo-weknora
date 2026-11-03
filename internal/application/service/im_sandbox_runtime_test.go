package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/skills"
	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type imTestBackend struct {
	sandbox.SessionFileStore
	calls   atomic.Int64
	writes  atomic.Int64
	invalid atomic.Bool
}

func TestSandboxPreparationErrorPreservesCauseAndStage(t *testing.T) {
	err := sandboxPreparationError("reconcile", context.DeadlineExceeded)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var stage interface{ SandboxPreparationStage() string }
	require.ErrorAs(t, err, &stage)
	require.Equal(t, "reconcile", stage.SandboxPreparationStage())
	require.NotContains(t, err.Error(), context.DeadlineExceeded.Error())
}

func (b *imTestBackend) EnsureIMSession(ctx context.Context, sid string, create bool) (string, error) {
	if sid != "s-1" || !create {
		return "", errors.New("bad binding")
	}
	p, ok := types.PrincipalFromContext(ctx)
	if !ok || p.ID != "native" || types.IMSandboxPolicyFromContext(ctx) != "policy" {
		b.invalid.Store(true)
	}
	return "container", nil
}
func (b *imTestBackend) StopIMSession(context.Context, string, string) error { return nil }
func (b *imTestBackend) ExecIMShell(
	_ context.Context, sid, id string, _, _ string, _ time.Duration, _ map[string]string,
) (*sandbox.ExecuteResult, error) {
	if sid != "s-1" || id != "container" {
		return nil, errors.New("cross-session")
	}
	b.calls.Add(1)
	return &sandbox.ExecuteResult{ExitCode: 0, Stdout: "backend executed"}, nil
}

func (b *imTestBackend) WriteSessionWorkspaceFiles(
	_ context.Context,
	sid string,
	files []sandbox.SessionWorkspaceFile,
) error {
	if sid != "s-1" || len(files) == 0 {
		return errors.New("invalid staging")
	}
	for _, file := range files {
		if !strings.HasPrefix(file.Path, "/workspace/.skills/alpha/") {
			return errors.New("unapproved staging path")
		}
	}
	b.writes.Add(1)
	return nil
}

func (*imTestBackend) ListIMSessionFiles(context.Context, string, string) ([]sandbox.RemoteDirEntry, error) {
	return nil, nil
}

func TestIMActualShellToolRevalidatesPinAndContext(t *testing.T) {
	db := newPinTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.TenantSandboxConfigEntity{}))
	require.NoError(
		t,
		db.Create(
			&types.TenantSandboxConfigEntity{
				ID:          "cfg",
				TenantID:    7,
				Name:        "test",
				SandboxType: "docker",
				Config:      &types.TenantSandboxConfig{SandboxType: "docker"},
			},
		).Error,
	)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(context.Background(), "s-1", SandboxPin{ConfigID: "cfg", TenantID: 7})
	require.NoError(t, err)
	var revoked atomic.Bool
	grant := types.IMSandboxGrant{
		SessionID:         "s-1",
		TenantID:          7,
		ConfigID:          "cfg",
		AllowShell:        true,
		AgentConfigDigest: "agent",
		Revalidate: func(ctx context.Context) error {
			if revoked.Load() {
				return types.ErrIMSandboxDenied
			}
			p, ok := types.PrincipalFromContext(ctx)
			if !ok || p.ID != "native" {
				return types.ErrIMSandboxDenied
			}
			return nil
		},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "s-1")
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalIMUser, ID: "native"})
	ctx = types.WithIMKnowledgeScope(ctx, []string{"kb"})
	ctx = types.WithIMSandboxGrant(ctx, grant)
	ctx = types.WithIMSandboxPolicy(ctx, "policy")
	hash, err := imSandboxConfigFingerprint(ctx, pinner, grant)
	require.NoError(t, err)
	backend := &imTestBackend{}
	guard := &imGuardedSandbox{backend: backend, pinner: pinner, configFingerprint: hash}
	shell := tools.NewShellExecTool(guard, nil)
	run := func(call context.Context, sid string) *types.ToolResult {
		call = tools.WithToolExecContext(call, &tools.ToolExecContext{SessionID: sid})
		r, e := shell.Execute(call, json.RawMessage(`{"command":"printf hello"}`))
		require.NoError(t, e)
		return r
	}
	result := run(ctx, "s-1")
	require.True(t, result.Success, result.Error)
	require.Equal(t, int64(1), backend.calls.Load())
	require.False(t, backend.invalid.Load())
	// Agent engine adds per-call child contexts in parallel. They must preserve
	// the originating native identity and grant rather than system-user secrets.
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child, cancel := context.WithTimeout(ctx, time.Second*10)
			defer cancel()
			child = tools.WithToolExecContext(child, &tools.ToolExecContext{SessionID: "s-1"})
			r, e := shell.Execute(child, json.RawMessage(`{"command":"printf hello"}`))
			results <- e == nil && r.Success
		}()
	}
	wg.Wait()
	close(results)
	for ok := range results {
		require.True(t, ok)
	}
	require.Equal(t, int64(3), backend.calls.Load())
	require.False(t, backend.invalid.Load())
	revoked.Store(true)
	require.False(t, run(ctx, "s-1").Success)
	revoked.Store(false)
	require.False(t, run(ctx, "other").Success)
	disabled := grant
	disabled.AllowShell = false
	require.False(t, run(types.WithIMSandboxGrant(ctx, disabled), "s-1").Success)
	require.NoError(t, db.Model(&types.Session{}).Where("id = ?", "s-1").Update("sandbox_config_id", "different").Error)
	require.False(t, run(ctx, "s-1").Success)
	require.Equal(t, int64(3), backend.calls.Load())
}

func TestIMPolicyFingerprintChangesWithSkillsAndAgent(t *testing.T) {
	grant := types.IMSandboxGrant{
		SessionID:         "s",
		TenantID:          7,
		ConfigID:          "cfg",
		AgentConfigDigest: "agent",
		AllowShell:        true,
		Revalidate:        func(context.Context) error { return nil },
	}
	ctx := types.WithIMSandboxGrant(context.Background(), grant)
	ctx = context.WithValue(ctx, imSandboxConfigBaselineKey{}, [32]byte{1})
	a, err := withIMSandboxPolicy(ctx)
	require.NoError(t, err)
	grant.AgentConfigDigest = "changed"
	b, err := withIMSandboxPolicy(types.WithIMSandboxGrant(ctx, grant))
	require.NoError(t, err)
	require.NotEqual(t, types.IMSandboxPolicyFromContext(a), types.IMSandboxPolicyFromContext(b))
	grant.AllowSkills = true
	_, err = withIMSandboxPolicy(types.WithIMSandboxGrant(ctx, grant))
	require.Error(t, err)
	c := context.WithValue(types.WithIMSandboxGrant(ctx, grant), imSkillFingerprintKey{}, [32]byte{2})
	c, err = withIMSandboxPolicy(c)
	require.NoError(t, err)
	d := context.WithValue(types.WithIMSandboxGrant(ctx, grant), imSkillFingerprintKey{}, [32]byte{3})
	d, err = withIMSandboxPolicy(d)
	require.NoError(t, err)
	require.NotEqual(t, types.IMSandboxPolicyFromContext(c), types.IMSandboxPolicyFromContext(d))
}

type imSkillRepoFixture struct {
	repository.TenantSkillRepository
	rows []*types.TenantSkillEntity
}

func (r *imSkillRepoFixture) ListSkillsByConfig(
	_ context.Context,
	tenant uint64,
	cfg string,
) ([]*types.TenantSkillEntity, error) {
	if tenant != 7 || cfg != "cfg" {
		return nil, errors.New("wrong config")
	}
	return r.rows, nil
}

type imConfigRepoFixture struct {
	repository.TenantSandboxConfigRepository
	cfg *types.TenantSandboxConfigEntity
}

func (r *imConfigRepoFixture) GetByID(
	_ context.Context,
	tenant uint64,
	cfg string,
) (*types.TenantSandboxConfigEntity, error) {
	if tenant != 7 || cfg != "cfg" {
		return nil, errors.New("wrong config")
	}
	return r.cfg, nil
}

func TestIMSkillSelectionAndCredentialIsolation(t *testing.T) {
	cfg := &types.TenantSandboxConfig{SandboxType: "docker", Docker: &types.DockerSandboxConfig{Image: "base"}}
	cfg.SkillImage = &types.SkillImageConfig{
		SnapshotID:       "installed",
		OwnerFingerprint: sandbox.SkillOwnerFingerprint(cfg),
	}
	rows := []*types.TenantSkillEntity{
		{
			ID:              "a",
			TenantID:        7,
			SandboxConfigID: "cfg",
			Name:            "alpha",
			Enabled:         true,
			Status:          types.SkillStatusReady,
			BundleSHA256:    strings.Repeat("a", 64),
			Envs:            types.SkillEnvVars{{Name: "TOKEN", Value: "system secret"}},
		},
		{
			ID:              "b",
			TenantID:        7,
			SandboxConfigID: "cfg",
			Name:            "beta",
			Enabled:         true,
			Status:          types.SkillStatusReady,
			BundleSHA256:    strings.Repeat("b", 64),
		},
		{
			ID:              "c",
			TenantID:        7,
			SandboxConfigID: "cfg",
			Name:            "off",
			Enabled:         false,
			Status:          types.SkillStatusReady,
			BundleSHA256:    strings.Repeat("c", 64),
		},
	}
	repo := &imSkillRepoFixture{rows: rows}
	svc := &sessionService{
		tenantSkillRepo: repo,
		sandboxConfigRepo: &imConfigRepoFixture{
			cfg: &types.TenantSandboxConfigEntity{ID: "cfg", TenantID: 7, Config: cfg},
		},
	}
	grant := types.IMSandboxGrant{
		SessionID:   "s-1",
		TenantID:    7,
		ConfigID:    "cfg",
		AllowSkills: true,
		Revalidate:  func(context.Context) error { return nil },
	}
	selected, err := svc.approvedIMSkills(context.Background(), grant, "selected", []string{"alpha"})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, "alpha", selected[0].Name)
	require.Empty(t, selected[0].Envs)
	require.NotEmpty(t, rows[0].Envs)
	grant.SkillsSelectionMode = "selected"
	grant.SelectedSkillNames = []string{"alpha"}
	runtimeConfig := &types.AgentConfig{SkillsEnabled: true}
	liveCtx, err := svc.withIMSkillsCheck(types.WithIMSandboxGrant(context.Background(), grant), runtimeConfig)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha"}, runtimeConfig.AllowedSkills)
	checker := liveCtx.Value(imSkillRevalidatorKey{}).(imSkillRevalidator)
	require.NoError(t, checker(liveCtx))
	repo.rows[0].Enabled = false
	require.Error(t, checker(liveCtx))
	repo.rows[0].Enabled = true
	all, err := svc.approvedIMSkills(context.Background(), grant, "all", nil)
	require.NoError(t, err)
	require.Len(t, all, 2)
	repo.rows[0].BundleSHA256 = ""
	_, err = svc.approvedIMSkills(context.Background(), grant, "selected", []string{"alpha"})
	require.ErrorContains(t, err, "digest")
}

func TestIMEnabledSkillCapabilityWithNoInstalledSkills(t *testing.T) {
	cfg := &types.TenantSandboxConfig{SandboxType: "docker", Docker: &types.DockerSandboxConfig{Image: "base"}}
	svc := &sessionService{
		tenantSkillRepo: &imSkillRepoFixture{},
		sandboxConfigRepo: &imConfigRepoFixture{
			cfg: &types.TenantSandboxConfigEntity{ID: "cfg", TenantID: 7, Config: cfg},
		},
	}
	grant := types.IMSandboxGrant{
		SessionID:           "s-1",
		TenantID:            7,
		ConfigID:            "cfg",
		AllowSkills:         true,
		SkillsSelectionMode: "all",
		Revalidate:          func(context.Context) error { return nil },
	}
	runtimeConfig := &types.AgentConfig{SkillsEnabled: true}
	ctx, err := svc.withIMSkillsCheck(types.WithIMSandboxGrant(context.Background(), grant), runtimeConfig)
	require.NoError(t, err)
	require.False(t, runtimeConfig.SkillsEnabled)
	require.Empty(t, runtimeConfig.TenantSkills)
	require.NoError(t, ctx.Value(imSkillRevalidatorKey{}).(imSkillRevalidator)(ctx))
}

func TestIMApprovedSkillToolStagesAndExecutesOnlySelected(t *testing.T) {
	db := newPinTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.TenantSandboxConfigEntity{}))
	require.NoError(
		t,
		db.Create(
			&types.TenantSandboxConfigEntity{
				ID:          "cfg",
				TenantID:    7,
				Name:        "test",
				SandboxType: "docker",
				Config:      &types.TenantSandboxConfig{SandboxType: "docker"},
			},
		).Error,
	)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(context.Background(), "s-1", SandboxPin{ConfigID: "cfg", TenantID: 7})
	require.NoError(t, err)
	var revoked atomic.Bool
	grant := types.IMSandboxGrant{
		SessionID:   "s-1",
		TenantID:    7,
		ConfigID:    "cfg",
		AllowSkills: true,
		Revalidate: func(context.Context) error {
			if revoked.Load() {
				return types.ErrIMSandboxDenied
			}
			return nil
		},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "s-1")
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalIMUser, ID: "native"})
	ctx = types.WithIMKnowledgeScope(ctx, []string{"kb"})
	ctx = types.WithIMSandboxGrant(ctx, grant)
	ctx = types.WithIMSandboxPolicy(ctx, "policy")
	ctx = withIMSkillRevalidator(ctx, func(context.Context) error {
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	})
	hash, err := imSandboxConfigFingerprint(ctx, pinner, grant)
	require.NoError(t, err)
	backend := &imTestBackend{}
	guard := &imGuardedSandbox{backend: backend, pinner: pinner, configFingerprint: hash}
	root := t.TempDir()
	dir := filepath.Join(root, "alpha")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: alpha\ndescription: Test approved skill\n---\n# Instructions\nRun the script.\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "run.py"), []byte("print('ok')"), 0o600))
	svc := &agentService{sandboxPinner: pinner}
	config := &types.AgentConfig{TenantSkills: []*types.TenantSkillEntity{{Name: "alpha"}}}
	check := func() error { return svc.authorizeIMSkillSource(ctx, "s-1") }
	require.ErrorIs(
		t,
		guard.WriteSessionWorkspaceFile(ctx, "s-1", "/workspace/.skills/alpha/run.py", []byte("modified")),
		types.ErrIMSandboxDenied,
	)
	require.ErrorIs(
		t,
		guard.WriteSessionWorkspaceFiles(
			ctx,
			"s-1",
			[]sandbox.SessionWorkspaceFile{{Path: "/workspace/.skills/alpha/run.py"}},
		),
		types.ErrIMSandboxDenied,
	)
	manager := skills.NewManager(
		&skills.ManagerConfig{Enabled: true, AllowedSkills: []string{"alpha"}, StrictStagedIntegrity: true}, guard,
	).
		WithTenantSource(imStagedSkillSource{SkillSource: skills.NewLoader([]string{root}), authorize: check}).
		WithTenantVisibilityGuard(check)
	require.NoError(t, manager.Initialize(ctx))
	require.Len(t, manager.GetAllMetadata(), 1)
	tool := tools.NewIMSkillExecTool(
		manager,
		guard,
		func(call context.Context, name string) error { return svc.authorizeIMSkill(call, "s-1", config, name) },
	)
	result, err := tool.Execute(ctx, json.RawMessage(`{"skill_name":"alpha","script":"run.py","args":["hello"]}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, int64(1), backend.writes.Load())
	require.Equal(t, int64(1), backend.calls.Load())
	result, err = tool.Execute(ctx, json.RawMessage(`{"skill_name":"beta","script":"run.py"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	result, err = tool.Execute(ctx, json.RawMessage(`{"skill_name":"alpha","script":"../run.py"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	revoked.Store(true)
	require.Empty(t, manager.GetAllMetadata())
	result, err = tool.Execute(ctx, json.RawMessage(`{"skill_name":"alpha","script":"run.py"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, int64(1), backend.calls.Load())
}

type imBlockingBackend struct {
	*imTestBackend
	started   chan struct{}
	cancelled chan struct{}
	failStop  bool
}

func (b *imBlockingBackend) ExecIMShell(
	ctx context.Context, _, _, _, _ string, _ time.Duration, _ map[string]string,
) (*sandbox.ExecuteResult, error) {
	close(b.started)
	<-ctx.Done()
	close(b.cancelled)
	if b.failStop {
		return nil, errors.Join(ctx.Err(), sandbox.ErrIMStopUnconfirmed, errors.New("must-not-escape-stderr"))
	}
	return nil, ctx.Err()
}

type imRevokingReadBackend struct {
	*imTestBackend
	revoked *atomic.Bool
}

func (b *imRevokingReadBackend) ReadSessionFileBounded(context.Context, string, string, int64) ([]byte, error) {
	b.revoked.Store(true)
	return []byte("must not escape"), nil
}

func TestIMRevocationDuringExecutionCancelsAndSuppressesOutput(t *testing.T) {
	db := newPinTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.TenantSandboxConfigEntity{}))
	require.NoError(
		t,
		db.Create(
			&types.TenantSandboxConfigEntity{
				ID:          "cfg",
				TenantID:    7,
				Name:        "test",
				SandboxType: "docker",
				Config:      &types.TenantSandboxConfig{SandboxType: "docker"},
			},
		).Error,
	)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(context.Background(), "s-1", SandboxPin{ConfigID: "cfg", TenantID: 7})
	require.NoError(t, err)
	var revoked atomic.Bool
	grant := types.IMSandboxGrant{
		SessionID:  "s-1",
		TenantID:   7,
		ConfigID:   "cfg",
		AllowShell: true,
		Revalidate: func(context.Context) error {
			if revoked.Load() {
				return types.ErrIMSandboxDenied
			}
			return nil
		},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "s-1")
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalIMUser, ID: "native"})
	ctx = types.WithIMKnowledgeScope(ctx, nil)
	ctx = types.WithIMSandboxGrant(ctx, grant)
	ctx = types.WithIMSandboxPolicy(ctx, "policy")
	hash, err := imSandboxConfigFingerprint(ctx, pinner, grant)
	require.NoError(t, err)
	backend := &imBlockingBackend{
		imTestBackend: &imTestBackend{},
		started:       make(chan struct{}),
		cancelled:     make(chan struct{}),
		failStop:      true,
	}
	guard := &imGuardedSandbox{backend: backend, pinner: pinner, configFingerprint: hash}
	type outcome struct {
		result *sandbox.ExecuteResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := guard.ExecShellCommand(ctx, "s-1", "long command", "/workspace", time.Minute, nil)
		done <- outcome{result, err}
	}()
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not start")
	}
	revoked.Store(true)
	select {
	case result := <-done:
		require.ErrorIs(t, result.err, sandbox.ErrIMStopUnconfirmed)
		require.NotContains(t, result.err.Error(), "must-not-escape-stderr")
		require.Nil(t, result.result)
	case <-time.After(5 * time.Second):
		t.Fatal("revocation did not cancel the running command")
	}
	select {
	case <-backend.cancelled:
	default:
		t.Fatal("backend was not cancelled")
	}
	revoked.Store(false)
	reader := &imRevokingReadBackend{imTestBackend: &imTestBackend{}, revoked: &revoked}
	guard.backend = reader
	data, err := guard.ReadSessionFile(ctx, "s-1", "/workspace/output/file.txt")
	require.Error(t, err)
	require.Nil(t, data, "file data read across revocation must not be returned")
}
