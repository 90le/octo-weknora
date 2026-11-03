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

func TestMarkdownExportRequiresRequestPermissionAndTrustedSink(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "把知识生成 md 文件精简给我"
	var calls int
	ctx := WithMarkdownSink(principalContext(p), func(ctx context.Context, file *ReportArtifact) error {
		calls++
		require.NoError(t, ValidateMarkdownAuthority(ctx))
		require.Equal(t, "knowledge-summary.md", file.Name)
		require.Equal(t, "text/markdown", file.ContentType)
		require.Equal(t, "# Synthetic\n", file.Content)
		return nil
	}, allowMarkdownScope)
	tool := NewMarkdownFileTool()
	result, err := tool.Execute(ctx, json.RawMessage(`{"content":"# Synthetic\n"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Output, `"delivered":true`)
	again, err := tool.Execute(ctx, json.RawMessage(`{"content":"# Synthetic\n"}`))
	require.NoError(t, err)
	require.False(t, again.Success)
	require.Equal(t, 1, calls, "one user message may produce only one file attempt")

	p.MessageText = "请回答知识库问题"
	ctx = WithMarkdownSink(principalContext(p), func(context.Context, *ReportArtifact) error {
		t.Fatal("file sent without an explicit request")
		return nil
	}, allowMarkdownScope)
	result, err = tool.Execute(ctx, json.RawMessage(`{"content":"hello"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	p.AllowGeneratedFiles = false
	p.MessageText = "请发送文件"
	ctx = WithMarkdownSink(principalContext(p), func(context.Context, *ReportArtifact) error {
		t.Fatal("file sent without permission")
		return nil
	}, allowMarkdownScope)
	result, err = tool.Execute(ctx, json.RawMessage(`{"content":"hello"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
}

func TestMarkdownExportRejectsRedirectOversizeAndChangedAuthority(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "请发 Markdown 文件"
	fresh := p
	p.Validate = func(context.Context) (Principal, error) { return fresh, nil }
	var sends int
	ctx := WithMarkdownSink(principalContext(p), func(ctx context.Context, _ *ReportArtifact) error {
		sends++
		fresh.AllowGeneratedFiles = false
		return ValidateMarkdownAuthority(ctx)
	}, allowMarkdownScope)
	tool := NewMarkdownFileTool()
	for _, raw := range []string{`{"content":"hello","recipient":"another"}`, `{"content":""}`, `{"content":"x"}{}`} {
		result, err := tool.Execute(ctx, json.RawMessage(raw))
		require.NoError(t, err)
		require.False(t, result.Success)
	}
	large, err := json.Marshal(map[string]string{"content": strings.Repeat("x", (1<<20)+1)})
	require.NoError(t, err)
	result, err := tool.Execute(ctx, large)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Zero(t, sends)
	result, err = tool.Execute(ctx, json.RawMessage(`{"content":"hello"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, ErrDenied.Error(), result.Error)
	require.Equal(t, 1, sends)
}

func TestMarkdownExportDoesNotRetryAmbiguousOrConcurrentDelivery(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "导出文件"
	var calls atomic.Int32
	ctx := WithMarkdownSink(principalContext(p), func(context.Context, *ReportArtifact) error {
		calls.Add(1)
		return errors.New("delivery outcome unknown")
	}, allowMarkdownScope)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := NewMarkdownFileTool().Execute(ctx, json.RawMessage(`{"content":"hello"}`))
			if err != nil || result.Success {
				t.Error("ambiguous delivery was claimed successful")
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestMarkdownExportRejectsExplicitNegativeRequests(t *testing.T) {
	for _, text := range []string{
		"不要发送文件", "不要把文件发出来", "不用发附件", "do not send a file", "don't export markdown", "这个文件内容是什么？",
	} {
		if explicitlyRequestsFile(text) {
			t.Fatalf("negative request permitted file delivery: %q", text)
		}
	}
	for _, text := range []string{"把知识生成 md 文件精简给我", "请导出 Markdown 文件", "please export a file"} {
		if !explicitlyRequestsFile(text) {
			t.Fatalf("explicit file request denied: %q", text)
		}
	}
}

func TestMarkdownExportRechecksCompleteScope(t *testing.T) {
	p := testPrincipal()
	p.AllowGeneratedFiles = true
	p.MessageText = "请发文件"
	allowed := true
	ctx := WithMarkdownSink(principalContext(p), func(ctx context.Context, _ *ReportArtifact) error {
		allowed = false // Simulate a management or web permission change during upload.
		return ValidateMarkdownAuthority(ctx)
	}, func(context.Context) error {
		if !allowed {
			return ErrDenied
		}
		return nil
	})
	result, err := NewMarkdownFileTool().Execute(ctx, json.RawMessage(`{"content":"data"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, ErrDenied.Error(), result.Error)
}
