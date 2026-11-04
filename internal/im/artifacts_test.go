package im

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/event"
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

type deliveredIMArtifactCollector struct{ fakeIMArtifactCollector }

func (*deliveredIMArtifactCollector) DiscardDeliveredText(context.Context, string, types.MessageArtifact) error {
	return nil
}

func (f *deliveredIMArtifactCollector) PersistDeliveredText(
	ctx context.Context, _, _ string, _ uint64, name string, data []byte,
	authorize func(context.Context) error,
) (types.MessageArtifact, error) {
	if err := authorize(ctx); err != nil {
		return types.MessageArtifact{}, err
	}
	return makeTestArtifact(name, data), nil
}

func TestIMRecordsConfirmedTextWithoutSandbox(t *testing.T) {
	scope := &ExecutionScope{
		AccountID: "a", ScopeID: "s", Revision: "r", KnowledgeBaseIDs: []string{"kb"},
		AllowFileDelivery: true,
	}
	adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
	req := &qaRequest{
		msg: &IncomingMessage{
			UserID: "u", MessageID: "m", Content: "发送文件 report.txt", ChatType: ChatTypeDirect,
			Extra: map[string]string{},
		},
		channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel", adapter: adapter,
	}
	s := &Service{artifactCollector: &deliveredIMArtifactCollector{}}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "session")
	ctx = s.businessContext(ctx, req, scope)
	result, err := octobusiness.NewFileDeliveryTool().Execute(
		ctx, json.RawMessage(`{"name":"report.txt","content":"hello"}`),
	)
	require.NoError(t, err)
	require.True(t, result.Success)
	confirmed := renderConfirmedTextDelivery(ctx, "文件已发送：report.txt")
	require.NotContains(t, confirmed, "其他文件")
	mixed := renderConfirmedTextDelivery(ctx, "文件已发送：report.txt 和 [下载](sandbox:missing.pptx)")
	require.Contains(t, mixed, "其他文件是否已生成或发送")
	require.NotContains(t, mixed, "sandbox:")
	msg := &types.Message{ID: "answer", SessionID: "session"}
	s.persistIMDeliveredText(ctx, msg)
	require.Len(t, msg.Artifacts, 1)
	require.Equal(t, "report.txt", msg.Artifacts[0].FileName)
	wrong := &types.Message{ID: "answer", SessionID: "other"}
	s.persistIMDeliveredText(ctx, wrong)
	require.Empty(t, wrong.Artifacts)
}

func (f *fakeIMArtifactCollector) SnapshotIM(
	context.Context, string, string, uint64, uint64, func(string) bool,
) (*appservice.IMArtifactSnapshot, error) {
	f.snapshots++
	return &appservice.IMArtifactSnapshot{}, nil
}

func (f *fakeIMArtifactCollector) CollectIM(
	_ context.Context,
	_ *appservice.IMArtifactSnapshot,
	_ string,
	selected func(string, bool) appservice.IMArtifactSelection,
) (types.MessageArtifacts, error) {
	f.collects++
	counts := map[string]int{}
	for _, artifact := range f.artifacts {
		counts[artifact.FileName]++
	}
	var result types.MessageArtifacts
	for _, artifact := range f.artifacts {
		relative := strings.TrimPrefix(artifact.SourcePath, "/workspace/output/")
		if relative == "" {
			relative = artifact.FileName
		}
		if selected(relative, counts[artifact.FileName] > 1) != appservice.IMArtifactNotSelected {
			result = append(result, artifact)
		}
	}
	return result, f.err
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
	sent        []string
	data        [][]byte
	fail        string
	packageName string
}

func (a *artifactTestAdapter) SendGeneratedFileWithResult(
	ctx context.Context, in *IncomingMessage, name string, data []byte, contentType string,
) (string, error) {
	if err := a.SendGeneratedFile(ctx, in, name, data, contentType); err != nil {
		return "", err
	}
	if a.packageName != "" {
		return a.packageName, nil
	}
	return name, nil
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
	private := makeTestArtifact("private.csv", []byte("private"))
	scope := &ExecutionScope{
		AccountID: "account", ScopeID: "scope", Revision: "r",
		KnowledgeBaseIDs: []string{"kb"}, AllowFileDelivery: true,
	}
	adapter := &artifactTestAdapter{
		scopeTestAdapter: &scopeTestAdapter{scope: scope}, fail: second.FileName, packageName: "example.pdf.zip",
	}
	agent := &types.CustomAgent{TenantID: 7}
	agent.Config.SandboxConfigID = "cfg"
	req := &qaRequest{
		msg: &IncomingMessage{
			UserID:    "user",
			MessageID: "message",
			Content:   "请生成文件并发送给我",
			ChatType:  ChatTypeDirect,
			Extra:     map[string]string{},
		},
		channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
		session: &types.Session{ID: "session", TenantID: 7}, agent: agent, adapter: adapter, scope: scope,
	}
	collector := &fakeIMArtifactCollector{artifacts: types.MessageArtifacts{first, second, private}}
	s := &Service{
		artifactCollector: collector,
		defaultFileSvc: &memoryIMArtifactFiles{
			files: map[string][]byte{first.URL: data, second.URL: []byte("hello")},
		},
	}
	ctx := s.businessContext(context.Background(), req, scope)
	ctx = imArtifactTestGrant(ctx)
	ctx = s.beginIMArtifactTurn(ctx, req)
	message := &types.Message{ID: "answer"}
	answer := s.finishIMArtifactTurn(ctx, message,
		"附件已发送。已整理：请点击下载：[下载PDF](sandbox:example.pdf)、![文本](sandbox:example.txt)。正文保留。")
	require.Equal(t, 1, collector.snapshots)
	require.Equal(t, 1, collector.collects)
	require.Equal(t, []string{"example.pdf"}, adapter.sent)
	require.Equal(t, data, adapter.data[0])
	require.Len(t, message.Artifacts, 2)
	require.Contains(t, answer, "已发送文件：example.pdf.zip（包含 example.pdf）")
	require.Contains(t, answer, "请下载 ZIP 压缩包并解压后查看其中的文件。")
	require.Contains(t, answer, "文件发送状态待确认")
	require.Contains(t, answer, "正文保留。")
	require.NotContains(t, answer, "请点击下载")
	require.NotContains(t, answer, "!文本")
	require.NotContains(t, answer, "sandbox:example.pdf")
	require.NotContains(t, answer, "sandbox:example.txt")
	require.Contains(t, answer, "文件发送状态未知，请先检查会话再重试：example.txt")
}

func TestRenderIMArtifactAnswer(t *testing.T) {
	tests := []struct {
		name, answer string
		names        []string
		uncertain    bool
		want         string
	}{
		{"image and download", "说明。请点击下载：![下载图片](sandbox:pic.png)！", []string{"pic.png"}, false, "说明。pic.png！"},
		{
			"other links untouched", "![图](sandbox:other.png) [报告](sandbox:report.pdf)",
			[]string{"report.pdf"},
			false, "![图](sandbox:other.png) 报告",
		},
		{"uncertain", "文件已发送。[下载](sandbox:report.pdf) 正常回答。", nil, true, "文件发送状态待确认。report.pdf 正常回答。"},
		{"ordinary punctuation", "注意！[报告](sandbox:report.pdf)", nil, false, "注意！报告"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, renderIMArtifactAnswer(tc.answer, tc.names, tc.uncertain))
		})
	}
}

func TestIMGroupCreationWithoutSendDoesNotStartCapture(t *testing.T) {
	scope := &ExecutionScope{
		AccountID:         "a",
		ScopeID:           "s",
		Revision:          "r",
		KnowledgeBaseIDs:  []string{"kb"},
		AllowFileDelivery: true,
	}
	adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
	collector := &fakeIMArtifactCollector{}
	req := &qaRequest{
		msg: &IncomingMessage{
			UserID:    "u",
			MessageID: "m",
			Content:   "只生成 report.pdf，保存在沙箱",
			ChatType:  ChatTypeGroup,
			Extra:     map[string]string{},
		},
		channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
		session: &types.Session{
			ID:       "session",
			TenantID: 7,
		}, agent: &types.CustomAgent{TenantID: 7}, adapter: adapter, scope: scope,
	}
	s := &Service{artifactCollector: collector}
	ctx := imArtifactTestGrant(s.businessContext(context.Background(), req, scope))
	ctx = s.beginIMArtifactTurn(ctx, req)
	_ = s.finishIMArtifactTurn(ctx, &types.Message{ID: "answer"}, "[报告](sandbox:report.pdf)")
	require.Zero(t, collector.snapshots)
	require.Empty(t, adapter.sent)
}

func TestIMArtifactTurnRechecksScopeAndContent(t *testing.T) {
	scope := &ExecutionScope{
		AccountID:         "a",
		ScopeID:           "s",
		Revision:          "r",
		KnowledgeBaseIDs:  []string{"kb"},
		AllowFileDelivery: true,
	}
	adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
	agent := &types.CustomAgent{TenantID: 7}
	agent.Config.SandboxConfigID = "cfg"
	req := &qaRequest{
		msg: &IncomingMessage{
			UserID:    "u",
			MessageID: "m",
			Content:   "请发送 file.txt",
			ChatType:  ChatTypeGroup,
			Extra:     map[string]string{},
		},
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

func TestIMFullOutputConnectsAgentCompletionToFileDelivery(t *testing.T) {
	data := []byte{0x25, 0x50, 0x44, 0x46, 0x00, 0xff}
	file := makeTestArtifact("sample.pdf", data)
	scope := &ExecutionScope{
		AccountID: "account", ScopeID: "scope", Revision: "r",
		KnowledgeBaseIDs: []string{"kb"}, AllowFileDelivery: true,
	}
	adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
	agent := &types.CustomAgent{TenantID: 7, Config: types.CustomAgentConfig{AgentMode: types.AgentModeSmartReasoning}}
	req := &qaRequest{
		msg: &IncomingMessage{
			UserID:    "user",
			MessageID: "question",
			Content:   "请生成 PDF 文件",
			ChatType:  ChatTypeDirect,
			Extra:     map[string]string{},
		},
		channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
		session: &types.Session{ID: "session", TenantID: 7}, agent: agent, adapter: adapter, scope: scope,
	}
	messages := &fullOutputMessageService{}
	s := &Service{
		artifactCollector: &fakeIMArtifactCollector{artifacts: types.MessageArtifacts{file}},
		defaultFileSvc:    &memoryIMArtifactFiles{files: map[string][]byte{file.URL: data}},
		messageService:    messages,
		streamManager:     &fullOutputStreamManager{},
		sessionService: &exitSessionService{run: func(ctx context.Context, bus *event.EventBus) error {
			return bus.Emit(ctx, event.Event{
				Type: event.EventAgentComplete,
				Data: event.AgentCompleteData{FinalAnswer: "已生成 [报告](sandbox:sample.pdf)"},
			})
		}},
	}
	ctx := imArtifactTestGrant(s.businessContext(context.Background(), req, scope))
	ctx = s.beginIMArtifactTurn(ctx, req)
	answer, err := s.runQA(ctx, req.session, req.msg.Content, agent, nil, nil, nil, "user", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"sample.pdf"}, adapter.sent)
	require.Equal(t, data, adapter.data[0])
	require.Contains(t, answer, "已发送文件：sample.pdf")
	require.NotContains(t, answer, "sandbox:sample.pdf")
	saved := messages.latestUpdate()
	require.NotNil(t, saved)
	require.Len(t, saved.Artifacts, 1)
	require.True(t, saved.IsCompleted)
}

func TestIMGroupNaturalRequestUsesReferencedFiles(t *testing.T) {
	scope := &ExecutionScope{
		AccountID:         "a",
		ScopeID:           "s",
		Revision:          "r",
		KnowledgeBaseIDs:  []string{"kb"},
		AllowFileDelivery: true,
	}
	adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
	report := makeTestArtifact("report.pdf", []byte("report"))
	private := makeTestArtifact("private.csv", []byte("private"))
	collector := &fakeIMArtifactCollector{artifacts: types.MessageArtifacts{report, private}}
	req := &qaRequest{
		msg: &IncomingMessage{
			UserID:    "u",
			MessageID: "m",
			Content:   "请生成 PDF 文件并发送给我",
			ChatType:  ChatTypeGroup,
			Extra:     map[string]string{},
		},
		channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
		session: &types.Session{
			ID:       "session",
			TenantID: 7,
		}, agent: &types.CustomAgent{TenantID: 7}, adapter: adapter, scope: scope,
	}
	s := &Service{
		artifactCollector: collector,
		defaultFileSvc: &memoryIMArtifactFiles{
			files: map[string][]byte{report.URL: []byte("report"), private.URL: []byte("private")},
		},
	}
	ctx := imArtifactTestGrant(s.businessContext(context.Background(), req, scope))
	ctx = s.beginIMArtifactTurn(ctx, req)
	answer := s.finishIMArtifactTurn(
		ctx,
		&types.Message{ID: "first"},
		"[报告](sandbox:report.pdf) [明细](sandbox:private.csv)",
	)
	require.Equal(t, []string{"report.pdf"}, adapter.sent)
	require.Contains(t, answer, "已发送文件")

	req.msg.Content = "请发送 report.pdf"
	ctx = imArtifactTestGrant(s.businessContext(context.Background(), req, scope))
	ctx = s.beginIMArtifactTurn(ctx, req)
	answer = s.finishIMArtifactTurn(ctx, &types.Message{ID: "second"}, "报告已准备。")
	require.Equal(t, []string{"report.pdf", "report.pdf"}, adapter.sent)
	require.Contains(t, answer, "已发送文件：report.pdf")
}

func TestIMGroupTemporaryAndAmbiguousNamesFailClosed(t *testing.T) {
	scope := &ExecutionScope{
		AccountID:         "a",
		ScopeID:           "s",
		Revision:          "r",
		KnowledgeBaseIDs:  []string{"kb"},
		AllowFileDelivery: true,
	}
	adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
	one := makeTestArtifact("report.pdf", []byte("one"))
	one.URL = "one"
	one.SourcePath = "/workspace/output/a/report.pdf"
	two := makeTestArtifact("report.pdf", []byte("two"))
	two.URL = "two"
	two.SourcePath = "/workspace/output/b/report.pdf"
	temp := makeTestArtifact("draft.csv", []byte("tmp"))
	temp.SourcePath = "/workspace/output/tmp/draft.csv"
	collector := &fakeIMArtifactCollector{artifacts: types.MessageArtifacts{one, two, temp}}
	req := &qaRequest{
		msg: &IncomingMessage{
			UserID:    "u",
			MessageID: "m",
			Content:   "请发送 report.pdf 和 draft.csv",
			ChatType:  ChatTypeGroup,
			Extra:     map[string]string{},
		},
		channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
		session: &types.Session{
			ID:       "session",
			TenantID: 7,
		}, agent: &types.CustomAgent{TenantID: 7}, adapter: adapter, scope: scope,
	}
	s := &Service{
		artifactCollector: collector,
		defaultFileSvc: &memoryIMArtifactFiles{
			files: map[string][]byte{"one": []byte("one"), "two": []byte("two"), temp.URL: []byte("tmp")},
		},
	}
	ctx := imArtifactTestGrant(s.businessContext(context.Background(), req, scope))
	ctx = s.beginIMArtifactTurn(ctx, req)
	_ = s.finishIMArtifactTurn(ctx, &types.Message{ID: "first"}, "答复")
	require.Empty(t, adapter.sent, "a basename cannot select multiple paths or an unrequested temporary path")

	req.msg.Content = "请发送 a/report.pdf 和 b/report.pdf"
	ctx = imArtifactTestGrant(s.businessContext(context.Background(), req, scope))
	ctx = s.beginIMArtifactTurn(ctx, req)
	_ = s.finishIMArtifactTurn(ctx, &types.Message{ID: "second"}, "答复")
	require.Equal(t, []string{"report.pdf", "report.pdf"}, adapter.sent)
	require.Equal(t, [][]byte{[]byte("one"), []byte("two")}, adapter.data)

	req.msg.Content = "请发送 tmp/draft.csv"
	ctx = imArtifactTestGrant(s.businessContext(context.Background(), req, scope))
	ctx = s.beginIMArtifactTurn(ctx, req)
	_ = s.finishIMArtifactTurn(ctx, &types.Message{ID: "third"}, "答复")
	require.Equal(t, []string{"report.pdf", "report.pdf", "draft.csv"}, adapter.sent)
	require.Equal(t, []byte("tmp"), adapter.data[2])
}

func TestIMGroupAmbiguousRootNeedsAnExplicitRelativePath(t *testing.T) {
	scope := &ExecutionScope{
		AccountID:         "a",
		ScopeID:           "s",
		Revision:          "r",
		KnowledgeBaseIDs:  []string{"kb"},
		AllowFileDelivery: true,
	}
	adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
	root := makeTestArtifact("report.pdf", []byte("root"))
	root.URL, root.SourcePath = "root", "/workspace/output/report.pdf"
	nested := makeTestArtifact("report.pdf", []byte("nested"))
	nested.URL, nested.SourcePath = "nested", "/workspace/output/sub/report.pdf"
	req := &qaRequest{
		msg: &IncomingMessage{
			UserID:    "u",
			MessageID: "m",
			Content:   "请发送 report.pdf",
			ChatType:  ChatTypeGroup,
			Extra:     map[string]string{},
		},
		channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
		session: &types.Session{
			ID:       "session",
			TenantID: 7,
		}, agent: &types.CustomAgent{TenantID: 7}, adapter: adapter, scope: scope,
	}
	s := &Service{
		artifactCollector: &fakeIMArtifactCollector{artifacts: types.MessageArtifacts{root, nested}},
		defaultFileSvc: &memoryIMArtifactFiles{
			files: map[string][]byte{"root": []byte("root"), "nested": []byte("nested")},
		},
	}
	ctx := imArtifactTestGrant(s.businessContext(context.Background(), req, scope))
	ctx = s.beginIMArtifactTurn(ctx, req)
	_ = s.finishIMArtifactTurn(ctx, &types.Message{ID: "first"}, "答复")
	require.Empty(t, adapter.sent)
	req.msg.Content = "请发送 ./report.pdf"
	ctx = imArtifactTestGrant(s.businessContext(context.Background(), req, scope))
	ctx = s.beginIMArtifactTurn(ctx, req)
	_ = s.finishIMArtifactTurn(ctx, &types.Message{ID: "second"}, "答复")
	require.Equal(t, []string{"report.pdf"}, adapter.sent)
	require.Equal(t, [][]byte{[]byte("root")}, adapter.data)
}

func TestIMPPTRequestCollectsAndDeliversRealOutput(t *testing.T) {
	for _, tc := range []struct {
		name, request, answer string
		chat                  ChatType
	}{
		{"direct-link", "请生成一个PPT", "已生成 [下载PPT](sandbox:mofang_pro_intro.pptx)", ChatTypeDirect},
		{"direct-name", "请生成一个PPT", "已生成 mofang_pro_intro.pptx。", ChatTypeDirect},
		{"group-link", "生成PPT给我", "已生成 [下载PPT](sandbox:mofang_pro_intro.pptx)", ChatTypeGroup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := &ExecutionScope{
				AccountID: "a", ScopeID: "s", Revision: "r", KnowledgeBaseIDs: []string{"kb"}, AllowFileDelivery: true,
			}
			adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
			file := makeTestArtifact("mofang_pro_intro.pptx", []byte("real presentation"))
			collector := &fakeIMArtifactCollector{artifacts: types.MessageArtifacts{file}}
			req := &qaRequest{
				msg:     &IncomingMessage{UserID: "u", MessageID: "m", Content: tc.request, ChatType: tc.chat},
				channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
				session: &types.Session{ID: "session", TenantID: 7}, agent: &types.CustomAgent{TenantID: 7},
				adapter: adapter, scope: scope,
			}
			s := &Service{
				artifactCollector: collector,
				defaultFileSvc:    &memoryIMArtifactFiles{files: map[string][]byte{file.URL: []byte("real presentation")}},
			}
			ctx := s.beginIMArtifactTurn(imArtifactTestGrant(s.businessContext(context.Background(), req, scope)), req)
			instruction, _ := ctx.Value(executionContextKey{}).(string)
			require.Contains(t, instruction, "/workspace/output/")
			result := s.finishIMArtifactTurn(ctx, &types.Message{ID: "answer"}, tc.answer)
			require.Equal(t, 1, collector.snapshots)
			require.Equal(t, []string{file.FileName}, adapter.sent)
			require.Contains(t, result, "已发送文件：mofang_pro_intro.pptx")
			require.NotContains(t, result, "sandbox:")
		})
	}
}

func TestIMUnconfirmedFileClaimsAndLinks(t *testing.T) {
	s := &Service{}
	req := &qaRequest{channel: &IMChannel{Platform: "octo"}, msg: &IncomingMessage{Content: "请生成PPT"}}
	ctx := s.beginIMArtifactTurn(context.Background(), req)
	answer := s.finishIMArtifactTurn(ctx, &types.Message{},
		"已生成并发送文件：[下载](sandbox:mofang_pro_intro.pptx)，备用 sandbox:missing.pptx")
	require.NotContains(t, answer, "sandbox:")
	require.NotContains(t, answer, "已生成")
	require.Contains(t, answer, "文件未确认生成或发送")
}

func TestIMPPTGroupAdmissionKeepsSendAndPermissionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		text    string
		allowed bool
		want    int
	}{
		{"生成PPT给我", true, 1},
		{"生成PPT，不要发送", true, 0},
		{"生成幻灯片", true, 0},
		{"生成PPT给我", false, 0},
	} {
		t.Run(tc.text, func(t *testing.T) {
			scope := &ExecutionScope{
				AccountID: "a", ScopeID: "s", Revision: "r", KnowledgeBaseIDs: []string{"kb"},
				AllowFileDelivery: tc.allowed,
			}
			adapter := &artifactTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: scope}}
			collector := &fakeIMArtifactCollector{}
			req := &qaRequest{
				msg:     &IncomingMessage{UserID: "u", MessageID: "m", Content: tc.text, ChatType: ChatTypeGroup},
				channel: &IMChannel{TenantID: 7, Platform: "octo"}, channelID: "channel",
				session: &types.Session{ID: "session", TenantID: 7}, agent: &types.CustomAgent{TenantID: 7},
				adapter: adapter, scope: scope,
			}
			s := &Service{artifactCollector: collector}
			_ = s.beginIMArtifactTurn(imArtifactTestGrant(s.businessContext(context.Background(), req, scope)), req)
			require.Equal(t, tc.want, collector.snapshots)
		})
	}
}
