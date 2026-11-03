package octobusiness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"path"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

type (
	fileDeliverySinkKey       struct{}
	fileDeliveryAuthorityKey  struct{}
	fileDeliveryExtraCheckKey struct{}
	legacyMarkdownCallKey     struct{}
)

type fileDeliveryAuthority struct {
	fingerprint string
	general     bool
}

func fileDeliveryCapability(p Principal, general bool) bool {
	if general {
		return p.AllowFileDelivery
	}
	return p.AllowGeneratedFiles
}

type fileDeliverySink struct {
	mu        sync.Mutex
	send      ReportSink
	validate  func(context.Context) error
	attempts  map[string]bool
	names     map[string]bool
	unknown   map[string]bool
	delivered map[string]bool
	files     []deliveredTextFile
	closed    bool
}

type deliveredTextFile struct {
	file      ReportArtifact
	authority fileDeliveryAuthority
	check     func(context.Context) error
	messageID string
	message   string
}

// DeliveredTextFiles returns copies of this turn's confirmed text deliveries.
// It rechecks the original authority and any operation-specific check; callers
// must retain the sink context and clear it when the turn ends.
func DeliveredTextFiles(ctx context.Context) ([]ReportArtifact, error) {
	state, ok := ctx.Value(fileDeliverySinkKey{}).(*fileDeliverySink)
	if !ok || state == nil || ctx.Err() != nil {
		return nil, ErrDenied
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil, ErrDenied
	}
	files := make([]ReportArtifact, 0, len(state.files))
	for _, item := range state.files {
		principal, present := PrincipalFromContext(ctx)
		if !present || principal.MessageID != item.messageID || principal.MessageText != item.message {
			return nil, ErrDenied
		}
		checkCtx := context.WithValue(ctx, fileDeliveryAuthorityKey{}, item.authority)
		if item.check != nil {
			checkCtx = context.WithValue(checkCtx, fileDeliveryExtraCheckKey{}, item.check)
		}
		if ValidateFileDeliveryAuthority(checkCtx) != nil || checkCtx.Err() != nil {
			return nil, ErrDenied
		}
		files = append(files, item.file)
	}
	return files, nil
}

// ClearDeliveredTextFiles drops retained content when the turn is finished.
func ClearDeliveredTextFiles(ctx context.Context) {
	state, ok := ctx.Value(fileDeliverySinkKey{}).(*fileDeliverySink)
	if !ok || state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	clear(state.files)
	state.files = nil
	state.closed = true
}

// WithFileDeliverySink enables bounded file delivery to the original turn.
func WithFileDeliverySink(ctx context.Context, sink ReportSink, validate func(context.Context) error) context.Context {
	if sink == nil || validate == nil {
		return ctx
	}
	return context.WithValue(ctx, fileDeliverySinkKey{}, &fileDeliverySink{
		send: sink, validate: validate, attempts: map[string]bool{}, names: map[string]bool{},
		unknown: map[string]bool{}, delivered: map[string]bool{},
	})
}

// HasDeliveredFile reports a confirmed successful tool delivery in this turn.
func HasDeliveredFile(ctx context.Context) bool {
	state, ok := ctx.Value(fileDeliverySinkKey{}).(*fileDeliverySink)
	if !ok || state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.delivered) > 0
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
	if err != nil || !fileDeliveryCapability(current, expected.general) ||
		reportAuthority(current) != expected.fingerprint {
		return ErrDenied
	}
	state, ok := ctx.Value(fileDeliverySinkKey{}).(*fileDeliverySink)
	if !ok || state == nil || state.validate == nil || state.validate(ctx) != nil {
		return ErrDenied
	}
	if check, ok := ctx.Value(fileDeliveryExtraCheckKey{}).(func(context.Context) error); ok && check(ctx) != nil {
		return ErrDenied
	}
	return nil
}

// WithFileDeliveryExtraCheck adds an operation-specific check, e.g. that a
// captured sandbox pin still belongs to the original session and agent.
func WithFileDeliveryExtraCheck(ctx context.Context, check func(context.Context) error) context.Context {
	if check == nil {
		return ctx
	}
	return context.WithValue(ctx, fileDeliveryExtraCheckKey{}, check)
}

// WithCurrentFileDeliveryAuthority binds an internally-produced artifact to
// the current verified principal. Callers must not supply a principal or
// recipient from model output.
func WithCurrentFileDeliveryAuthority(ctx context.Context, general bool) (context.Context, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !fileDeliveryCapability(p, general) || !HasFileDeliverySink(ctx) {
		return ctx, ErrDenied
	}
	ctx = context.WithValue(
		ctx,
		fileDeliveryAuthorityKey{},
		fileDeliveryAuthority{fingerprint: reportAuthority(p), general: general},
	)
	return ctx, ValidateFileDeliveryAuthority(ctx)
}

// WantsFileDelivery identifies an explicit request before automatic artifact
// publication. It is deliberately conservative about negative requests.
func WantsFileDelivery(text string) bool { return explicitlyRequestsFile(text) }

// WantsExplicitFileSend is used for group delivery, where merely asking to
// create a file must not publish it to everyone in the conversation.
func WantsExplicitFileSend(text string) bool {
	if !explicitlyRequestsFile(text) {
		return false
	}
	text = strings.ToLower(text)
	for _, word := range []string{"发送", "发给", "发我", "发到", "发出来", "请发", "传给", "给我", "投递", "send", "attach", "deliver"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// HasNamedArtifact distinguishes a user's concrete selection from a generic
// request, so model links cannot silently add other files to that selection.
func HasNamedArtifact(text string) bool { return namedArtifactInText.MatchString(text) }

// WantsAllFiles is deliberately not interpreted as a directory-wide grant.
func WantsAllFiles(text string) bool {
	text = strings.ToLower(text)
	for _, phrase := range []string{
		"所有文件", "全部文件", "所有附件", "全部附件", "所有产物", "全部产物", "所有输出", "全部输出", "发全部",
		"发所有", "all files", "every file", "everything",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// MatchesRequestedArtifactType narrows a generic request when it names a file
// format. A request with no format still requires a concrete final link.
func MatchesRequestedArtifactType(text, name string) bool {
	text, ext := strings.ToLower(text), strings.ToLower(path.Ext(name))
	categories := []struct {
		words []string
		exts  []string
	}{
		{[]string{"word", "docx"}, []string{".doc", ".docx"}},
		{[]string{"excel", "xlsx", "表格"}, []string{".xls", ".xlsx", ".csv"}},
		{[]string{"pdf"}, []string{".pdf"}},
		{[]string{"ppt", "powerpoint", "演示文稿"}, []string{".ppt", ".pptx"}},
		{[]string{"markdown", ".md"}, []string{".md"}},
		{[]string{"csv"}, []string{".csv"}},
		{[]string{"json"}, []string{".json"}},
		{[]string{"图片", "png", "jpeg", "jpg"}, []string{".png", ".jpg", ".jpeg"}},
	}
	found, match := false, false
	for _, category := range categories {
		mentioned := false
		for _, word := range category.words {
			mentioned = mentioned || strings.Contains(text, word)
		}
		if mentioned {
			found = true
			for _, extension := range category.exts {
				match = match || ext == extension
			}
		}
	}
	return !found || match
}

// MentionsArtifactName matches a concrete file name or relative path in the
// original user text. A suffix inside a different filename is not a selection.
// Common Chinese action words are accepted without requiring spaces.
func MentionsArtifactName(text, name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\r\n\x00") {
		return false
	}
	for offset := 0; offset <= len(text)-len(name); {
		i := strings.Index(text[offset:], name)
		if i < 0 {
			return false
		}
		i += offset
		before, after := text[:i], text[i+len(name):]
		if artifactBoundary(before, true) && artifactBoundary(after, false) {
			return true
		}
		offset = i + len(name)
	}
	return false
}

func artifactBoundary(text string, before bool) bool {
	if text == "" {
		return true
	}
	var r rune
	if before {
		r, _ = utf8.DecodeLastRuneInString(text)
	} else {
		r, _ = utf8.DecodeRuneInString(text)
	}
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-/\\", r) {
		return true
	}
	if before {
		for _, word := range []string{"发送", "发给", "请发", "生成", "制作", "导出", "选择", "发", "和", "及", "与", "把", "将"} {
			if strings.HasSuffix(text, word) {
				return true
			}
		}
	} else {
		for _, word := range []string{"发给", "发我", "发送", "给我", "和", "与", "以及", "还有"} {
			if strings.HasPrefix(text, word) {
				return true
			}
		}
	}
	return false
}

func requestsMultipleVersions(text string) bool {
	text = strings.ToLower(text)
	for _, phrase := range []string{
		"两个版本", "两种版本", "两版", "多个版本", "不同版本", "多版本", "两份", "two versions",
		"multiple versions", "both versions",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

type fileDeliveryTool struct{}

// legacyMarkdownFileTool accepts old saved tool calls but is registered as a
// deferred compatibility alias, so it is not offered as a separate capability.
type legacyMarkdownFileTool struct{}

var namedArtifactInText = regexp.MustCompile(`[\p{L}\p{N}_-][\p{L}\p{N}_.-]*\.[\p{L}\p{N}]{1,16}`)

// NewLegacyMarkdownFileTool returns the compatibility alias for Markdown file delivery.
func NewLegacyMarkdownFileTool() types.Tool  { return &legacyMarkdownFileTool{} }
func (*legacyMarkdownFileTool) Name() string { return "octo_send_markdown" }
func (*legacyMarkdownFileTool) Description() string {
	return "Compatibility alias for older Markdown tool calls."
}

func (*legacyMarkdownFileTool) Parameters() json.RawMessage {
	return json.RawMessage(
		`{"type":"object","additionalProperties":false,"properties":{"content":{"type":"string"}},"re` +
			`quired":["content"]}`,
	)
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
	return (&fileDeliveryTool{}).Execute(context.WithValue(ctx, legacyMarkdownCallKey{}, true), forward)
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
		"文件", "附件", "压缩包", "电子表格", "演示文稿", "图片", "音频", "视频",
		"markdown", ".md", ".txt", ".csv", ".json", ".html", ".zip", ".7z", ".rar",
		".png", ".jpg", ".jpeg", ".svg", ".mp3", ".wav", ".mp4",
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
		// "分别", "区别" and "特别" are not prohibitions.
		negationText := strings.NewReplacer("分别", "", "区别", "", "特别", "").Replace(clause)
		for _, word := range []string{"不要", "不用", "不需要", "无需", "禁止", "别", "do not", "don't", "no need"} {
			negative = negative || strings.Contains(negationText, word)
		}
		// Negation may refer to the file requested in an earlier clause:
		// "生成文件，不要发送" must not authorize an outbound attachment.
		if negative && (hasFile || strings.ContainsAny(clause, "发传给送") ||
			strings.Contains(clause, "导出") || strings.Contains(clause, "投递") ||
			strings.Contains(clause, "上传") || strings.Contains(clause, "分享") ||
			strings.Contains(clause, "公开") || strings.Contains(clause, "对外") ||
			strings.Contains(clause, "交付") || strings.Contains(clause, "send") ||
			strings.Contains(clause, "attach") || strings.Contains(clause, "deliver") ||
			strings.Contains(clause, "export") || strings.Contains(clause, "upload") ||
			strings.Contains(clause, "share")) {
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
		return toolResult(
			nil,
			argumentError("Use a plain file name with extension and nonempty UTF-8 content up to 1 MiB."),
		)
	}
	legacyCall, _ := ctx.Value(legacyMarkdownCallKey{}).(bool)
	legacyMode := p.AllowGeneratedFiles && strings.EqualFold(path.Ext(name), ".md") &&
		(!p.AllowFileDelivery || legacyCall || (!p.IsDirect && !MentionsArtifactName(p.MessageText, name) &&
			!namedArtifactInText.MatchString(p.MessageText)))
	if legacyMode {
		// The old grant allowed one fixed-name Markdown file per message.
		name = "knowledge-summary.md"
	} else if !p.AllowFileDelivery {
		return toolResult(nil, ErrDenied)
	} else if !p.IsDirect && (!WantsExplicitFileSend(p.MessageText) || (HasNamedArtifact(p.MessageText) &&
		!MentionsArtifactName(p.MessageText, name)) || !MatchesRequestedArtifactType(p.MessageText, name)) {
		return toolResult(nil, argumentError("In a group, request the file to send."))
	}
	ext := strings.ToLower(path.Ext(name))
	contentType := mime.TypeByExtension(ext)
	// Windows may register CSV as an Excel binary MIME type. These formats are
	// still bounded UTF-8 text, regardless of the host's MIME registry.
	switch ext {
	case ".md":
		contentType = "text/markdown; charset=utf-8"
	case ".csv":
		contentType = "text/csv; charset=utf-8"
	}
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	if !strings.HasPrefix(contentType, "text/") && !strings.Contains(contentType, "json") &&
		!strings.Contains(contentType, "xml") && !strings.Contains(contentType, "yaml") &&
		!strings.Contains(contentType, "toml") {
		return toolResult(nil, argumentError("This tool only produces text; generate binary files in the sandbox."))
	}
	state := ctx.Value(fileDeliverySinkKey{}).(*fileDeliverySink)
	state.mu.Lock()
	defer state.mu.Unlock()
	hash := sha256.Sum256([]byte(input.Content))
	attempt := name + "\x00" + hex.EncodeToString(hash[:])
	if state.closed || state.attempts[attempt] || state.unknown[name] || len(state.attempts) >= 5 ||
		(legacyMode && len(state.attempts) != 0) || (!p.IsDirect && !HasNamedArtifact(p.MessageText) &&
		len(state.attempts) != 0) || (state.names[name] && !requestsMultipleVersions(p.MessageText)) {
		return toolResult(nil, errors.New("this file was already attempted or the per-message limit was reached"))
	}
	ctx, err = WithCurrentFileDeliveryAuthority(ctx, !legacyMode)
	if err != nil {
		return toolResult(nil, err)
	}
	state.attempts[attempt] = true
	state.names[name] = true
	file := &ReportArtifact{Name: name, Content: input.Content, ContentType: contentType}
	if err := state.send(ctx, file); err != nil {
		if outcome, ok := err.(interface{ DeliveryOutcome() string }); !ok || outcome.DeliveryOutcome() != "failed" {
			state.unknown[name] = true
		}
		return toolResult(nil, err)
	}
	if ctx.Err() != nil || ValidateFileDeliveryAuthority(ctx) != nil {
		state.unknown[name] = true
		return toolResult(nil, ErrDenied)
	}
	authority := ctx.Value(fileDeliveryAuthorityKey{}).(fileDeliveryAuthority)
	check, _ := ctx.Value(fileDeliveryExtraCheckKey{}).(func(context.Context) error)
	state.files = append(state.files, deliveredTextFile{
		file:      ReportArtifact{Name: name, Content: input.Content, ContentType: contentType},
		authority: authority, check: check,
		messageID: p.MessageID, message: p.MessageText,
	})
	state.delivered[name] = true
	return toolResult(map[string]any{"delivered": true, "name": file.Name}, nil)
}
