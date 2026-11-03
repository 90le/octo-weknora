package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type imParallelFixture struct{ calls atomic.Int64 }

func (p *imParallelFixture) check(ctx context.Context, sid string) error {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	identity, has := types.PrincipalFromContext(ctx)
	session, hasSession := types.SessionIDFromContext(ctx)
	if !ok || !has || !hasSession || sid != "s" || session != "s" || identity.ID != "native" || types.IMSandboxPolicyFromContext(ctx) != "policy" || !types.HasIMKnowledgeScope(ctx) {
		return errors.New("IM context missing")
	}
	if err := grant.Revalidate(ctx); err != nil {
		return err
	}
	p.calls.Add(1)
	return nil
}
func (p *imParallelFixture) ExecShellCommand(ctx context.Context, sid, command, workdir string, timeout time.Duration, env map[string]string) (*sandbox.ExecuteResult, error) {
	if err := p.check(ctx, sid); err != nil {
		return nil, err
	}
	return &sandbox.ExecuteResult{Stdout: "sandbox"}, nil
}
func (p *imParallelFixture) StatSessionFile(ctx context.Context, sid, path string) (*sandbox.RemoteStatEntry, error) {
	if err := p.check(ctx, sid); err != nil {
		return nil, err
	}
	return &sandbox.RemoteStatEntry{Type: sandbox.RemoteEntryFile, Size: 4}, nil
}
func (p *imParallelFixture) ReadSessionFile(ctx context.Context, sid, path string) ([]byte, error) {
	if err := p.check(ctx, sid); err != nil {
		return nil, err
	}
	return []byte("data"), nil
}
func (p *imParallelFixture) ListSessionFiles(ctx context.Context, sid, path string) ([]sandbox.RemoteDirEntry, error) {
	if err := p.check(ctx, sid); err != nil {
		return nil, err
	}
	return nil, nil
}

func TestIMContextSurvivesParallelEngineAndRealTools(t *testing.T) {
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = tools.NewToolRegistry()
	backend := &imParallelFixture{}
	engine.toolRegistry.RegisterTool(tools.NewReadFileTool(backend))
	engine.toolRegistry.RegisterTool(tools.NewShellExecTool(backend, nil))
	grant := types.IMSandboxGrant{SessionID: "s", TenantID: 7, ConfigID: "cfg", AllowShell: true, Revalidate: func(context.Context) error { return nil }}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "s")
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalIMUser, ID: "native"})
	ctx = types.WithIMKnowledgeScope(ctx, nil)
	ctx = types.WithIMSandboxGrant(ctx, grant)
	ctx = types.WithIMSandboxPolicy(ctx, "policy")
	response := &types.ChatResponse{ToolCalls: []types.LLMToolCall{
		{ID: "a", Function: types.FunctionCall{Name: tools.ToolReadFile, Arguments: `{"path":"/workspace/a.txt"}`}},
		{ID: "b", Function: types.FunctionCall{Name: tools.ToolReadFile, Arguments: `{"path":"/workspace/b.txt"}`}},
		{ID: "c", Function: types.FunctionCall{Name: tools.ToolShellExec, Arguments: `{"command":"printf ok"}`}},
	}}
	step := &types.AgentStep{}
	engine.executeToolCallsParallel(ctx, response, step, 0, "s", "message")
	require.Len(t, step.ToolCalls, 3)
	for _, call := range step.ToolCalls {
		require.True(t, call.Result.Success, call.Result.Error)
	}
	require.GreaterOrEqual(t, backend.calls.Load(), int64(5))
}
