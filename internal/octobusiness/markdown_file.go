package octobusiness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

type (
	markdownSinkKey      struct{}
	markdownAuthorityKey struct{}
)

type markdownSink struct {
	mu        sync.Mutex
	send      ReportSink
	validate  func(context.Context) error
	attempted bool
}

// WithMarkdownSink enables one generated file attempt in the original turn.
func WithMarkdownSink(ctx context.Context, sink ReportSink, validate func(context.Context) error) context.Context {
	if sink == nil || validate == nil {
		return ctx
	}
	return context.WithValue(ctx, markdownSinkKey{}, &markdownSink{send: sink, validate: validate})
}

// HasMarkdownSink reports whether the native channel admitted file generation.
func HasMarkdownSink(ctx context.Context) bool {
	sink, ok := ctx.Value(markdownSinkKey{}).(*markdownSink)
	return ok && sink != nil && sink.send != nil && sink.validate != nil
}

// ValidateMarkdownAuthority is repeated before and after uploading a file.
func ValidateMarkdownAuthority(ctx context.Context) error {
	expected, ok := ctx.Value(markdownAuthorityKey{}).(string)
	if !ok {
		return nil
	}
	current, err := currentPrincipal(ctx)
	if err != nil || !current.AllowGeneratedFiles || reportAuthority(current) != expected {
		return ErrDenied
	}
	state, ok := ctx.Value(markdownSinkKey{}).(*markdownSink)
	if !ok || state == nil || state.validate == nil || state.validate(ctx) != nil {
		return ErrDenied
	}
	return nil
}

type markdownFileTool struct{}

// NewMarkdownFileTool creates a native, current-conversation-only export tool.
func NewMarkdownFileTool() types.Tool { return &markdownFileTool{} }

func (*markdownFileTool) Name() string { return "octo_send_markdown" }

func (*markdownFileTool) Description() string {
	return "Send one UTF-8 Markdown file to the current Octo conversation only when the user explicitly " +
		"requests a file or attachment. Supply the complete file content, including relevant source " +
		"references. Do not choose a recipient or read a server path. Only claim delivery after success."
}

func (*markdownFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":` +
		`{"content":{"type":"string","minLength":1,"description":"Complete Markdown content."}},` +
		`"required":["content"]}`)
}

func explicitlyRequestsFile(text string) bool {
	text = strings.ToLower(text)
	fileWords := []string{"文件", "附件", "markdown", ".md", "attachment", "file"}
	requestWords := []string{
		"发给", "发送", "发我", "发文件", "发附件", "发 markdown", "发md", "发 .md", "请发", "传给", "给我",
		"生成", "导出", "下载", "保存", "输出", "整理成", "做成", "转成", "制作",
		"send", "attach", "export", "download", "generate", "create", "save", "write",
	}
	requested := false
	for _, clause := range strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune("，。！？；,!?;\n", r)
	}) {
		hasFile, hasRequest := false, false
		for _, word := range fileWords {
			hasFile = hasFile || strings.Contains(clause, word)
		}
		for _, word := range requestWords {
			hasRequest = hasRequest || strings.Contains(clause, word)
		}
		negative := false
		for _, word := range []string{"不要", "不用", "不需要", "无需", "禁止", "别", "do not", "don't", "no need"} {
			negative = negative || strings.Contains(clause, word)
		}
		if negative && (hasFile || strings.Contains(clause, "导出") || strings.Contains(clause, "export")) {
			return false
		}
		requested = requested || (hasFile && hasRequest) ||
			strings.Contains(clause, "导出") || strings.Contains(clause, "export")
	}
	return requested
}

func (*markdownFileTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.AllowGeneratedFiles || !HasMarkdownSink(ctx) {
		return toolResult(nil, ErrDenied)
	}
	if !explicitlyRequestsFile(p.MessageText) {
		return toolResult(nil, argumentError("The user must explicitly request a file or attachment."))
	}
	var input struct {
		Content string `json:"content"`
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		return toolResult(nil, argumentError("Supply exactly one object with Markdown content only."))
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || !utf8.ValidString(input.Content) ||
		strings.ContainsRune(input.Content, 0) || strings.TrimSpace(input.Content) == "" || len(input.Content) > 1<<20 {
		return toolResult(nil, argumentError("Markdown content must be nonempty UTF-8 and at most 1 MiB."))
	}
	state := ctx.Value(markdownSinkKey{}).(*markdownSink)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.attempted {
		return toolResult(nil, errors.New("a file was already attempted for this message; do not resend automatically"))
	}
	ctx = context.WithValue(ctx, markdownAuthorityKey{}, reportAuthority(p))
	if err := ValidateMarkdownAuthority(ctx); err != nil {
		return toolResult(nil, err)
	}
	state.attempted = true
	file := &ReportArtifact{Name: "knowledge-summary.md", Content: input.Content, ContentType: "text/markdown"}
	if err := state.send(ctx, file); err != nil {
		return toolResult(nil, err)
	}
	return toolResult(map[string]any{"delivered": true, "name": file.Name}, nil)
}
