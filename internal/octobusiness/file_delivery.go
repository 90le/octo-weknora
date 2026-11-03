package octobusiness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"path"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

type (
	fileDeliverySinkKey      struct{}
	fileDeliveryAuthorityKey struct{}
)

type fileDeliveryAuthority struct {
	fingerprint string
	general     bool
}

type fileDeliverySink struct {
	mu       sync.Mutex
	send     ReportSink
	validate func(context.Context) error
	attempts map[string]bool
}

// WithFileDeliverySink enables bounded file delivery to the original turn.
func WithFileDeliverySink(ctx context.Context, sink ReportSink, validate func(context.Context) error) context.Context {
	if sink == nil || validate == nil {
		return ctx
	}
	return context.WithValue(ctx, fileDeliverySinkKey{}, &fileDeliverySink{send: sink, validate: validate, attempts: map[string]bool{}})
}

// HasFileDeliverySink reports whether the native channel admitted delivery.
func HasFileDeliverySink(ctx context.Context) bool {
	sink, ok := ctx.Value(fileDeliverySinkKey{}).(*fileDeliverySink)
	return ok && sink != nil && sink.send != nil && sink.validate != nil
}

// ValidateFileDeliveryAuthority is repeated before and after uploading a file.
func ValidateFileDeliveryAuthority(ctx context.Context) error {
	expected, ok := ctx.Value(fileDeliveryAuthorityKey{}).(fileDeliveryAuthority)
	if !ok {
		return nil
	}
	current, err := currentPrincipal(ctx)
	if err != nil || (!current.AllowFileDelivery && (expected.general || !current.AllowGeneratedFiles)) ||
		reportAuthority(current) != expected.fingerprint {
		return ErrDenied
	}
	state, ok := ctx.Value(fileDeliverySinkKey{}).(*fileDeliverySink)
	if !ok || state == nil || state.validate == nil || state.validate(ctx) != nil {
		return ErrDenied
	}
	return nil
}

// WithCurrentFileDeliveryAuthority binds an internally-produced artifact to
// the current verified principal. Callers must not supply a principal or
// recipient from model output.
func WithCurrentFileDeliveryAuthority(ctx context.Context, general bool) (context.Context, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || (!p.AllowFileDelivery && (general || !p.AllowGeneratedFiles)) || !HasFileDeliverySink(ctx) {
		return ctx, ErrDenied
	}
	ctx = context.WithValue(ctx, fileDeliveryAuthorityKey{}, fileDeliveryAuthority{fingerprint: reportAuthority(p), general: general})
	return ctx, ValidateFileDeliveryAuthority(ctx)
}

// WantsFileDelivery identifies an explicit request before automatic artifact
// publication. It is deliberately conservative about negative requests.
func WantsFileDelivery(text string) bool { return explicitlyRequestsFile(text) }

type fileDeliveryTool struct{}

// legacyMarkdownFileTool accepts old saved tool calls but is registered as a
// deferred compatibility alias, so it is not offered as a separate capability.
type legacyMarkdownFileTool struct{}

func NewLegacyMarkdownFileTool() types.Tool  { return &legacyMarkdownFileTool{} }
func (*legacyMarkdownFileTool) Name() string { return "octo_send_markdown" }
func (*legacyMarkdownFileTool) Description() string {
	return "Compatibility alias for older Markdown tool calls."
}
func (*legacyMarkdownFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"content":{"type":"string"}},"required":["content"]}`)
}
func (*legacyMarkdownFileTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	var input struct {
		Content string `json:"content"`
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		return toolResult(nil, argumentError("Invalid legacy file arguments."))
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return toolResult(nil, argumentError("Invalid legacy file arguments."))
	}
	forward, err := json.Marshal(map[string]string{"name": "knowledge-summary.md", "content": input.Content})
	if err != nil {
		return toolResult(nil, err)
	}
	return (&fileDeliveryTool{}).Execute(ctx, forward)
}

// NewFileDeliveryTool sends small text artifacts without requiring a sandbox.
// Binary artifacts come from the session sandbox's collected output instead.
func NewFileDeliveryTool() types.Tool { return &fileDeliveryTool{} }

func (*fileDeliveryTool) Name() string { return "deliver_text_file" }

func (*fileDeliveryTool) Description() string {
	return "Deliver a small UTF-8 text file to the current conversation only when explicitly requested. " +
		"Use the requested filename and complete text with relevant source references. For binary files, " +
		"generate them in the session sandbox output directory instead. Do not choose a recipient or " +
		"read a server path. Only claim delivery after success."
}

func (*fileDeliveryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":` +
		`{"name":{"type":"string","minLength":1,"description":"File name with extension, without a path."},` +
		`"content":{"type":"string","minLength":1,"description":"Complete UTF-8 text content."}},` +
		`"required":["name","content"]}`)
}

func explicitlyRequestsFile(text string) bool {
	text = strings.ToLower(text)
	fileWords := []string{
		"文件", "附件", "markdown", ".md", ".txt", ".csv", ".json", ".html",
		"pdf", "docx", "xlsx", "pptx", "word", "excel", "powerpoint", "attachment", "file",
	}
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

func (*fileDeliveryTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || (!p.AllowGeneratedFiles && !p.AllowFileDelivery) || !HasFileDeliverySink(ctx) {
		return toolResult(nil, ErrDenied)
	}
	if !explicitlyRequestsFile(p.MessageText) {
		return toolResult(nil, argumentError("The user must explicitly request a file or attachment."))
	}
	var input struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		return toolResult(nil, argumentError("Supply exactly one object with a file name and text content."))
	}
	var extra any
	name := strings.TrimSpace(input.Name)
	if decoder.Decode(&extra) != io.EOF || name == "" || name == "." || name != path.Base(name) ||
		strings.ContainsAny(name, "\\\r\n\x00") || path.Ext(name) == "" || len(name) > 255 ||
		!utf8.ValidString(name) || !utf8.ValidString(input.Content) ||
		strings.ContainsRune(input.Content, 0) || strings.TrimSpace(input.Content) == "" || len(input.Content) > 1<<20 {
		return toolResult(nil, argumentError("Use a plain file name with extension and nonempty UTF-8 content up to 1 MiB."))
	}
	state := ctx.Value(fileDeliverySinkKey{}).(*fileDeliverySink)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.attempts[name] || len(state.attempts) >= 5 {
		return toolResult(nil, errors.New("this file was already attempted or the per-message limit was reached"))
	}
	ctx, err = WithCurrentFileDeliveryAuthority(ctx, !strings.EqualFold(path.Ext(name), ".md"))
	if err != nil {
		return toolResult(nil, err)
	}
	state.attempts[name] = true
	contentType := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	if !strings.HasPrefix(contentType, "text/") && !strings.Contains(contentType, "json") &&
		!strings.Contains(contentType, "xml") && !strings.Contains(contentType, "yaml") &&
		!strings.Contains(contentType, "toml") {
		return toolResult(nil, argumentError("This tool only produces text; generate binary files in the sandbox."))
	}
	file := &ReportArtifact{Name: name, Content: input.Content, ContentType: contentType}
	if err := state.send(ctx, file); err != nil {
		return toolResult(nil, err)
	}
	return toolResult(map[string]any{"delivered": true, "name": file.Name}, nil)
}
