package octobusiness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func allowMarkdownScope(context.Context) error { return nil }

func TestFileDeliveryRequiresRequestPermissionAndTrustedSink(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "把知识生成 md 文件精简给我"
	var calls int
	ctx := WithFileDeliverySink(principalContext(p), func(ctx context.Context, file *ReportArtifact) error {
		calls++
		require.NoError(t, ValidateFileDeliveryAuthority(ctx))
		require.Equal(t, "knowledge-summary.md", file.Name)
		require.True(t, strings.HasPrefix(file.ContentType, "text/markdown") || strings.HasPrefix(file.ContentType, "text/plain"))
		require.Equal(t, "# Synthetic\n", file.Content)
		return nil
	}, allowMarkdownScope)
	tool := NewFileDeliveryTool()
	result, err := tool.Execute(ctx, json.RawMessage(`{"name":"knowledge-summary.md","content":"# Synthetic\n"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Output, `"delivered":true`)
	again, err := tool.Execute(ctx, json.RawMessage(`{"name":"knowledge-summary.md","content":"# Synthetic\n"}`))
	require.NoError(t, err)
	require.False(t, again.Success)
	require.Equal(t, 1, calls, "one user message may produce only one file attempt")

	p.MessageText = "请回答知识库问题"
	ctx = WithFileDeliverySink(principalContext(p), func(context.Context, *ReportArtifact) error {
		t.Fatal("file sent without an explicit request")
		return nil
	}, allowMarkdownScope)
	result, err = tool.Execute(ctx, json.RawMessage(`{"name":"knowledge-summary.md","content":"hello"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	p.AllowGeneratedFiles = false
	p.MessageText = "请发送文件"
	ctx = WithFileDeliverySink(principalContext(p), func(context.Context, *ReportArtifact) error {
		t.Fatal("file sent without permission")
		return nil
	}, allowMarkdownScope)
	result, err = tool.Execute(ctx, json.RawMessage(`{"name":"knowledge-summary.md","content":"hello"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
}

func TestFileDeliveryRejectsRedirectOversizeAndChangedAuthority(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "请发 Markdown 文件"
	fresh := p
	p.Validate = func(context.Context) (Principal, error) { return fresh, nil }
	var sends int
	ctx := WithFileDeliverySink(principalContext(p), func(ctx context.Context, _ *ReportArtifact) error {
		sends++
		fresh.AllowGeneratedFiles = false
		return ValidateFileDeliveryAuthority(ctx)
	}, allowMarkdownScope)
	tool := NewFileDeliveryTool()
	for _, raw := range []string{`{"name":"knowledge-summary.md","content":"hello","recipient":"another"}`, `{"name":"knowledge-summary.md","content":""}`, `{"name":"knowledge-summary.md","content":"x"}{}`} {
		result, err := tool.Execute(ctx, json.RawMessage(raw))
		require.NoError(t, err)
		require.False(t, result.Success)
	}
	large, err := json.Marshal(map[string]string{"name": "knowledge-summary.md", "content": strings.Repeat("x", (1<<20)+1)})
	require.NoError(t, err)
	result, err := tool.Execute(ctx, large)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Zero(t, sends)
	result, err = tool.Execute(ctx, json.RawMessage(`{"name":"knowledge-summary.md","content":"hello"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, ErrDenied.Error(), result.Error)
	require.Equal(t, 1, sends)
}

func TestFileDeliveryDoesNotRetryAmbiguousOrConcurrentDelivery(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "导出文件"
	var calls atomic.Int32
	ctx := WithFileDeliverySink(principalContext(p), func(context.Context, *ReportArtifact) error {
		calls.Add(1)
		return errors.New("delivery outcome unknown")
	}, allowMarkdownScope)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"knowledge-summary.md","content":"hello"}`))
			if err != nil || result.Success {
				t.Error("ambiguous delivery was claimed successful")
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestFileDeliveryRejectsExplicitNegativeRequests(t *testing.T) {
	for _, text := range []string{
		"不要发送文件", "不要把文件发出来", "不用发附件", "do not send a file", "don't export markdown", "这个文件内容是什么？",
		"生成文件，不要发送", "生成文件，先不要发", "只在沙箱生成文件，不要发到群里", "生成文件，不要分享", "生成文件，不要给任何人", "create a file, do not send it",
	} {
		if explicitlyRequestsFile(text) {
			t.Fatalf("negative request permitted file delivery: %q", text)
		}
	}
	for _, text := range []string{
		"把知识生成 md 文件精简给我", "请导出 Markdown 文件", "please export a file",
		"生成一份 PDF 给我", "请把内容做成 .zip 发送", "制作图片并发给我",
		"请分别发送 report.txt 和 data.csv",
	} {
		if !explicitlyRequestsFile(text) {
			t.Fatalf("explicit file request denied: %q", text)
		}
	}
}

func TestExplicitDoNotSendStopsTheLegacyTool(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "生成 Markdown 文件，不要发送"
	ctx := WithFileDeliverySink(principalContext(p), func(context.Context, *ReportArtifact) error {
		t.Fatal("a negative request reached the sender")
		return nil
	}, allowMarkdownScope)
	result, err := NewLegacyMarkdownFileTool().Execute(ctx, json.RawMessage(`{"content":"draft"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
}

func TestFileDeliveryRechecksCompleteScope(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "请发文件"
	allowed := true
	ctx := WithFileDeliverySink(principalContext(p), func(ctx context.Context, _ *ReportArtifact) error {
		allowed = false // Simulate a management or web permission change during upload.
		return ValidateFileDeliveryAuthority(ctx)
	}, func(context.Context) error {
		if !allowed {
			return ErrDenied
		}
		return nil
	})
	result, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"knowledge-summary.md","content":"data"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, ErrDenied.Error(), result.Error)
}

func TestFileDeliverySupportsTextFormatsAndRejectsPathsAndBinaryLabels(t *testing.T) {
	p := testPrincipal()
	p.AllowFileDelivery = true
	p.IsDirect = true
	p.MessageText = "请发送文件"
	var names []string
	ctx := WithFileDeliverySink(principalContext(p), func(_ context.Context, file *ReportArtifact) error {
		names = append(names, file.Name)
		return nil
	}, allowMarkdownScope)
	tool := NewFileDeliveryTool()
	for _, raw := range []string{
		`{"name":"notes.md","content":"# notes"}`,
		`{"name":"data.csv","content":"a,b\\n1,2"}`,
		`{"name":"data.json","content":"{}"}`,
	} {
		result, err := tool.Execute(ctx, json.RawMessage(raw))
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
	}
	for _, raw := range []string{
		`{"name":"../private.txt","content":"x"}`,
		`{"name":"C:\\\\private.txt","content":"x"}`,
		`{"name":"fake.pdf","content":"not a pdf"}`,
	} {
		result, err := tool.Execute(ctx, json.RawMessage(raw))
		require.NoError(t, err)
		require.False(t, result.Success)
	}
	require.Equal(t, []string{"notes.md", "data.csv", "data.json"}, names)
}

func TestLegacyMarkdownGrantDoesNotEnableOtherFormats(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "请发文件"
	var names []string
	ctx := WithFileDeliverySink(principalContext(p), func(_ context.Context, file *ReportArtifact) error {
		names = append(names, file.Name)
		return nil
	}, allowMarkdownScope)
	tool := NewFileDeliveryTool()
	denied, err := tool.Execute(ctx, json.RawMessage(`{"name":"data.csv","content":"a,b"}`))
	require.NoError(t, err)
	require.False(t, denied.Success)
	allowed, err := tool.Execute(ctx, json.RawMessage(`{"name":"notes.md","content":"# notes"}`))
	require.NoError(t, err)
	require.True(t, allowed.Success)
	require.Equal(t, []string{"knowledge-summary.md"}, names)
}

func TestFileDeliveryLegacyOnlyKeepsOneFixedMarkdownAttempt(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "请发送 Markdown 文件"
	var sent []string
	ctx := WithFileDeliverySink(principalContext(p), func(_ context.Context, file *ReportArtifact) error {
		sent = append(sent, file.Name)
		return nil
	}, allowMarkdownScope)
	first, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"first.md","content":"one"}`))
	require.NoError(t, err)
	require.True(t, first.Success, first.Error)
	second, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"second.md","content":"two"}`))
	require.NoError(t, err)
	require.False(t, second.Success)
	require.Equal(t, []string{"knowledge-summary.md"}, sent)
}

func TestLegacyAliasStillUsesItsOwnGrantWhenGeneralIsEnabled(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles, p.AllowFileDelivery = true, true
	p.MessageText = "请发送 Markdown 文件"
	fresh := p
	p.Validate = func(context.Context) (Principal, error) { return fresh, nil }
	var calls int
	ctx := WithFileDeliverySink(principalContext(p), func(ctx context.Context, file *ReportArtifact) error {
		calls++
		require.Equal(t, "knowledge-summary.md", file.Name)
		fresh.AllowGeneratedFiles = false
		return ValidateFileDeliveryAuthority(ctx)
	}, allowMarkdownScope)
	result, err := NewLegacyMarkdownFileTool().Execute(ctx, json.RawMessage(`{"content":"hello"}`))
	require.NoError(t, err)
	require.False(t, result.Success, "revoking the legacy grant must stop its own call")
	require.Equal(t, 1, calls)
}

func TestGroupTextDeliveryNeedsAConcreteUserSelection(t *testing.T) {
	p := testPrincipal()
	p.AllowFileDelivery = true
	p.MessageText = "请发送 report.txt"
	var sent []string
	ctx := WithFileDeliverySink(principalContext(p), func(_ context.Context, file *ReportArtifact) error {
		sent = append(sent, file.Name)
		return nil
	}, allowMarkdownScope)
	other, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"private.txt","content":"secret"}`))
	require.NoError(t, err)
	require.False(t, other.Success)
	allowed, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"report.txt","content":"report"}`))
	require.NoError(t, err)
	require.True(t, allowed.Success, allowed.Error)
	require.Equal(t, []string{"report.txt"}, sent)
}

func TestGeneralGroupCallCannotUseLegacyFallbackToChangeTheSelectedName(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles, p.AllowFileDelivery = true, true
	p.MessageText = "请发送 report.md"
	var sent []string
	ctx := WithFileDeliverySink(principalContext(p), func(_ context.Context, file *ReportArtifact) error {
		sent = append(sent, file.Name)
		return nil
	}, allowMarkdownScope)
	wrong, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"private.md","content":"private"}`))
	require.NoError(t, err)
	require.False(t, wrong.Success)
	correct, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"report.md","content":"report"}`))
	require.NoError(t, err)
	require.True(t, correct.Success, correct.Error)
	require.Equal(t, []string{"report.md"}, sent)
}

func TestSameNameVersionsNeedRequestAndDoNotRepeatUnknown(t *testing.T) {
	p := testPrincipal()
	p.IsDirect, p.AllowFileDelivery = true, true
	p.MessageText = "请发送 report.txt 的两个版本"
	var sent []string
	ctx := WithFileDeliverySink(principalContext(p), func(_ context.Context, file *ReportArtifact) error {
		sent = append(sent, file.Content)
		return nil
	}, allowMarkdownScope)
	tool := NewFileDeliveryTool()
	for _, content := range []string{"version one", "version two"} {
		args, err := json.Marshal(map[string]string{"name": "report.txt", "content": content})
		require.NoError(t, err)
		result, err := tool.Execute(ctx, args)
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
	}
	duplicate, err := tool.Execute(ctx, json.RawMessage(`{"name":"report.txt","content":"version one"}`))
	require.NoError(t, err)
	require.False(t, duplicate.Success)
	require.Equal(t, []string{"version one", "version two"}, sent)

	var attempts int
	unknownCtx := WithFileDeliverySink(principalContext(p), func(context.Context, *ReportArtifact) error {
		attempts++
		return errors.New("outcome unknown")
	}, allowMarkdownScope)
	_, _ = tool.Execute(unknownCtx, json.RawMessage(`{"name":"report.txt","content":"version one"}`))
	_, _ = tool.Execute(unknownCtx, json.RawMessage(`{"name":"report.txt","content":"version two"}`))
	require.Equal(t, 1, attempts, "an unknown outcome must not trigger another version automatically")
}

func TestMentionsArtifactNameRejectsSubstringMatches(t *testing.T) {
	for _, tc := range []struct {
		text, name string
		want       bool
	}{
		{"请发送 report.pdf 和 data.csv", "report.pdf", true},
		{"请发送报告.pdf给我", "报告.pdf", true},
		{"请发送 a/report.pdf 和 b/report.pdf", "a/report.pdf", true},
		{"请发送 private-report.pdf", "report.pdf", false},
		{"请发送私密报告.pdf", "报告.pdf", false},
		{"请发送 report.pdf.bak", "report.pdf", false},
		{"请发送 REPORT.pdf", "report.pdf", false},
	} {
		t.Run(tc.text+"/"+tc.name, func(t *testing.T) { require.Equal(t, tc.want, MentionsArtifactName(tc.text, tc.name)) })
	}
}

func TestLegacyMarkdownCallUsesUnifiedDelivery(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "请发 Markdown 文件"
	var file *ReportArtifact
	ctx := WithFileDeliverySink(principalContext(p), func(_ context.Context, sent *ReportArtifact) error {
		file = sent
		return nil
	}, allowMarkdownScope)
	result, err := NewLegacyMarkdownFileTool().Execute(ctx, json.RawMessage(`{"content":"# legacy"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "knowledge-summary.md", file.Name)
	require.Equal(t, "# legacy", file.Content)
}

func TestGroupNaturalTextFileRequest(t *testing.T) {
	p := testPrincipal()
	p.AllowFileDelivery = true
	p.MessageText = "请生成一份文件并发给我"
	var sent []string
	ctx := WithFileDeliverySink(principalContext(p), func(_ context.Context, f *ReportArtifact) error { sent = append(sent, f.Name); return nil }, allowMarkdownScope)
	result, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"report.txt","content":"result"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.True(t, HasDeliveredFile(ctx))
	second, err := NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"other.txt","content":"other"}`))
	require.NoError(t, err)
	require.False(t, second.Success)
	require.Equal(t, []string{"report.txt"}, sent)
}
