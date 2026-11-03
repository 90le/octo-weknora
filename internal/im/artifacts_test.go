package im

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type fakeIMArtifactCollector struct {
	artifacts types.MessageArtifacts
	err       error
	snapshots int
	collects  int
}

func (f *fakeIMArtifactCollector) SnapshotIM(
	context.Context, string, string, uint64, uint64,
) (*appservice.IMArtifactSnapshot, error) {
	f.snapshots++
	return &appservice.IMArtifactSnapshot{}, nil
}

func (f *fakeIMArtifactCollector) CollectIM(
	context.Context, *appservice.IMArtifactSnapshot, string,
) (types.MessageArtifacts, error) {
	f.collects++
	return f.artifacts, f.err
}

func (f *fakeIMArtifactCollector) ValidateIM(context.Context, *appservice.IMArtifactSnapshot, bool) error {
	return nil
}

func imArtifactTestGrant(ctx context.Context) context.Context {
	return types.WithIMSandboxGrant(ctx, types.IMSandboxGrant{
		SessionID: "session", TenantID: 7, ConfigID: "cfg",
		Revalidate: func(context.Context) error { return nil },
	})
}

type memoryIMArtifactFiles struct {
	*stubIMFileService
	files map[string][]byte
}

func (f *memoryIMArtifactFiles) GetFile(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := f.files[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type artifactTestAdapter struct {
	*scopeTestAdapter
	sent []string
	data [][]byte
	fail string
}

func (a *artifactTestAdapter) SendGeneratedFile(
	ctx context.Context, _ *IncomingMessage, name string, data []byte, _ string,
) error {
	if err := octobusiness.ValidateFileDeliveryAuthority(ctx); err != nil {
		return err
	}
	if name == a.fail {
		return errors.New("send failed")
	}
	a.sent = append(a.sent, name)
	a.data = append(a.data, append([]byte(nil), data...))
	return nil
}

func makeTestArtifact(name string, data []byte) types.MessageArtifact {
	digest := sha256.Sum256(data)
	return types.MessageArtifact{
		URL: name, FileName: name, FileSize: int64(len(data)), ContentHash: hex.EncodeToString(digest[:]),
	}
}

func TestIMArtifactTurnDeliversBoundedBinaryAndReportsPartialFailure(t *testing.T) {
	data := []byte{0, 1, 255, 2}
	first := makeTestArtifact("example.pdf", data)
	second := makeTestArtifact("example.txt", []byte("hello"))
	scope := &ExecutionScope{
		AccountID: "account", ScopeID: "scope", Revision: "r",
		KnowledgeBaseIDs: []string{"kb"}, AllowFileDelivery: true,
	}
	adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}, fail: second.FileName}
	agent := &types.CustomAgent{TenantID: 7}
	agent.Config.SandboxConfigID = "cfg"
	req := &qaRequest{
		msg:     &IncomingMessage{UserID: "user", MessageID: "message", Content: "请生成文件并发送给我", Extra: map[string]string{}},
		channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
		session: &types.Session{ID: "session", TenantID: 7}, agent: agent, adapter: adapter, scope: scope,
	}
	collector := &fakeIMArtifactCollector{artifacts: types.MessageArtifacts{first, second}}
	s := &Service{
		artifactCollector: collector,
		defaultFileSvc:    &memoryIMArtifactFiles{files: map[string][]byte{first.URL: data, second.URL: []byte("hello")}},
	}
	ctx := s.businessContext(context.Background(), req, scope)
	ctx = imArtifactTestGrant(ctx)
	ctx = s.beginIMArtifactTurn(ctx, req)
	message := &types.Message{ID: "answer"}
	answer := s.finishIMArtifactTurn(ctx, message, "已整理。")
	require.Equal(t, 1, collector.snapshots)
	require.Equal(t, 1, collector.collects)
	require.Equal(t, []string{"example.pdf"}, adapter.sent)
	require.Equal(t, data, adapter.data[0])
	require.Len(t, message.Artifacts, 2)
	require.Contains(t, answer, "已发送文件：example.pdf")
	require.Contains(t, answer, "文件发送状态未知，请先检查会话再重试：example.txt")
}

func TestIMArtifactTurnRechecksScopeAndContent(t *testing.T) {
	scope := &ExecutionScope{AccountID: "a", ScopeID: "s", Revision: "r", KnowledgeBaseIDs: []string{"kb"}, AllowFileDelivery: true}
	adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
	agent := &types.CustomAgent{TenantID: 7}
	agent.Config.SandboxConfigID = "cfg"
	req := &qaRequest{
		msg:     &IncomingMessage{UserID: "u", MessageID: "m", Content: "请发文件", Extra: map[string]string{}},
		channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
		session: &types.Session{ID: "session", TenantID: 7}, agent: agent, adapter: adapter, scope: scope,
	}
	file := makeTestArtifact("file.txt", []byte("expected"))
	collector := &fakeIMArtifactCollector{artifacts: types.MessageArtifacts{file}}
	s := &Service{artifactCollector: collector, defaultFileSvc: &memoryIMArtifactFiles{
		files: map[string][]byte{file.URL: []byte("modified")},
	}}
	ctx := s.businessContext(context.Background(), req, scope)
	ctx = imArtifactTestGrant(ctx)
	ctx = s.beginIMArtifactTurn(ctx, req)
	answer := s.finishIMArtifactTurn(ctx, &types.Message{ID: "answer"}, "答复")
	require.Contains(t, answer, "文件发送失败")
	require.Empty(t, adapter.sent, "changed bytes must never leave the workspace")

	ctx = s.businessContext(context.Background(), req, scope)
	ctx = imArtifactTestGrant(ctx)
	ctx = s.beginIMArtifactTurn(ctx, req)
	scope.AllowFileDelivery = false
	answer = s.finishIMArtifactTurn(ctx, &types.Message{ID: "answer-2"}, "答复")
	require.True(t, strings.Contains(answer, "未完成"))
	require.Equal(t, 1, collector.collects, "revocation must prevent collection")
	require.Empty(t, adapter.sent)
}
