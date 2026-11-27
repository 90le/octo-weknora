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

// ExplicitlyProhibitsFileDelivery reports whether the user explicitly forbade
// sending or exporting files (e.g. "不要发送文件", "只在沙箱保存", "do not send").
func ExplicitlyProhibitsFileDelivery(text string) bool {
	text = strings.ToLower(text)
	for _, clause := range strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune("，。！？；,!?;\n", r)
	}) {
		if strings.Contains(clause, "保存在沙箱") || strings.Contains(clause, "仅保存") ||
			strings.Contains(clause, "只保存") || strings.Contains(clause, "不要发到") ||
			strings.Contains(clause, "别发到") || strings.Contains(clause, "不发送") {
			return true
		}
		negationText := strings.NewReplacer("分别", "", "区别", "", "特别", "").Replace(clause)
		negative := false
		for _, word := range []string{"不要", "不用", "不需要", "无需", "禁止", "别", "do not", "don't", "no need"} {
			if strings.Contains(negationText, word) {
				negative = true
				break
			}
		}
		if !negative {
			continue
		}
		if hasFileKeyword(clause) || strings.ContainsAny(clause, "发传给送") ||
			strings.Contains(clause, "导出") || strings.Contains(clause, "投递") ||
			strings.Contains(clause, "上传") || strings.Contains(clause, "分享") ||
			strings.Contains(clause, "公开") || strings.Contains(clause, "对外") ||
			strings.Contains(clause, "交付") || strings.Contains(clause, "send") ||
			strings.Contains(clause, "attach") || strings.Contains(clause, "deliver") ||
			strings.Contains(clause, "export") || strings.Contains(clause, "upload") ||
			strings.Contains(clause, "share") {
			return true
		}
	}
	return false
}

// WantsFileDelivery identifies an explicit request before automatic artifact
// publication. It is deliberately conservative about negative requests.
func WantsFileDelivery(text string) bool { return explicitlyRequestsFile(text) }

// WantsExplicitFileSend is used for group delivery, recognizing broad natural
// phrasings while still respecting explicit do-not-send prohibitions.
func WantsExplicitFileSend(text string) bool {
	if ExplicitlyProhibitsFileDelivery(text) {
		return false
	}
	if !explicitlyRequestsFile(text) {
		return false
	}
	lower := strings.ToLower(text)
	for _, word := range []string{
		"发送", "发给", "发我", "发到", "发出来", "发群", "放群", "请发", "传给", "传我", "给我",
		"投递", "导出", "下载", "打包", "出一份", "出份", "做一份", "做份", "写一份", "写份",
		"生成", "制作", "整理成", "做成", "转成", "输出",
		"send", "attach", "deliver", "export", "download", "generate", "create",
	} {
		if strings.Contains(lower, word) {
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

// IsOpenEndedMultiFileRequest reports whether the user asked for an open-ended
// set of multiple files (e.g. "包括不限于等15种文件类型"), where mentioning an
// example filename or format must not exclude other deliverables created in the
// same turn.
func IsOpenEndedMultiFileRequest(text string) bool {
	if WantsAllFiles(text) {
		return true
	}
	lower := strings.ToLower(text)
	for _, phrase := range []string{
		"不限于", "包括但不限于", "包括不限于", "等", "多种", "多个", "各类", "不同格式", "各种格式",
		"and more", "etc", "various formats", "multiple formats",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// MatchesRequestedArtifactType checks whether a generated file matches the
// format(s) requested by the user. Supports all common document, web, media,
// archive, and code formats, plus direct extension mentions in the prompt.
func MatchesRequestedArtifactType(text, name string) bool {
	text, ext := strings.ToLower(text), strings.ToLower(path.Ext(name))
	if ext != "" {
		bareExt := strings.TrimPrefix(ext, ".")
		// Direct extension mention in user text (e.g. "html", ".html", "pptx", "svg", "yaml") always matches.
		if bareExt != "" && (strings.Contains(text, ext) || containsWordOrToken(text, bareExt)) {
			return true
		}
	}
	categories := []struct {
		words []string
		exts  []string
	}{
		{[]string{"word", "docx", "doc"}, []string{".doc", ".docx", ".rtf"}},
		{[]string{"excel", "xlsx", "xls", "表格", "电子表格", "数据表"}, []string{".xls", ".xlsx", ".csv"}},
		{[]string{"pdf"}, []string{".pdf"}},
		{[]string{"ppt", "pptx", "powerpoint", "演示文稿", "幻灯片", "slide", "slides", "deck"}, []string{".ppt", ".pptx", ".pdf"}},
		{[]string{"html", "htm", "网页", "页面", "h5"}, []string{".html", ".htm"}},
		{[]string{"markdown", ".md", "md文件", "md文档"}, []string{".md", ".markdown"}},
		{[]string{"txt", "纯文本", "文本文件"}, []string{".txt", ".md"}},
		{[]string{"csv"}, []string{".csv"}},
		{[]string{"json"}, []string{".json"}},
		{[]string{"yaml", "yml"}, []string{".yaml", ".yml"}},
		{[]string{"xml"}, []string{".xml"}},
		{[]string{"svg", "矢量图"}, []string{".svg"}},
		{[]string{"压缩包", "打包", "归档", "zip", "tar", "archive"}, []string{".zip", ".tar", ".gz", ".tgz", ".7z", ".rar"}},
		{[]string{"图片", "图表", "流程图", "脑图", "思维导图", "png", "jpeg", "jpg", "webp", "gif", "image", "chart", "diagram"}, []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg"}},
		{[]string{"音频", "mp3", "wav", "audio"}, []string{".mp3", ".wav", ".m4a", ".ogg", ".flac"}},
		{[]string{"视频", "mp4", "video"}, []string{".mp4", ".mov", ".webm", ".mkv"}},
		{[]string{"代码", "脚本", "sql", "python", "script", "code"}, []string{".py", ".js", ".ts", ".go", ".sh", ".sql", ".java", ".cpp", ".c", ".h", ".css", ".html", ".json", ".yaml", ".yml"}},
	}
	found, match := false, false
	for _, category := range categories {
		mentioned := false
		for _, word := range category.words {
			if len(word) <= 3 && isASCIIAlnum(word) {
				mentioned = mentioned || containsWordOrToken(text, word)
			} else {
				mentioned = mentioned || strings.Contains(text, word)
			}
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

func isASCIIAlnum(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// containsWordOrToken matches ASCII token without splitting another ASCII word
// (e.g. "html" matches "生成html和pptx", "html文件", ".html", but "md" does not match "cmd").
func containsWordOrToken(text, token string) bool {
	if token == "" {
		return false
	}
	for offset := 0; offset <= len(text)-len(token); {
		idx := strings.Index(text[offset:], token)
		if idx < 0 {
			return false
		}
		idx += offset
		leftOK := idx == 0 || !isASCIIWordByte(text[idx-1])
		rightIdx := idx + len(token)
		rightOK := rightIdx == len(text) || !isASCIIWordByte(text[rightIdx])
		if leftOK && rightOK {
			return true
		}
		offset = idx + len(token)
	}
	return false
}

func isASCIIWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
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

func hasFileKeyword(clause string) bool {
	fileWords := []string{
		"文件", "附件", "压缩包", "电子表格", "表格", "数据表", "演示文稿", "幻灯片", "文档", "报告",
		"网页", "页面", "图片", "图表", "流程图", "脑图", "思维导图", "音频", "视频", "代码", "脚本", "清单",
		"markdown", ".md", ".txt", ".csv", ".json", ".html", ".htm", ".zip", ".7z", ".rar", ".tar",
		".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".mp3", ".wav", ".mp4",
		".yaml", ".yml", ".xml", ".sql", ".py", ".js", ".ts",
		"pdf", "docx", "xlsx", "pptx", "ppt", "html", "htm", "svg", "csv", "json", "yaml", "zip",
		"word", "excel", "powerpoint", "attachment", "file", "slides", "slide", "deck", "report", "document",
	}
	for _, word := range fileWords {
		if len(word) <= 3 && isASCIIAlnum(word) {
			if containsWordOrToken(clause, word) {
				return true
			}
		} else if strings.Contains(clause, word) {
			return true
		}
	}
	return false
}

func explicitlyRequestsFile(text string) bool {
	if ExplicitlyProhibitsFileDelivery(text) {
		return false
	}
	text = strings.ToLower(text)
	requestWords := []string{
		"发给", "发送", "发我", "发文件", "发附件", "发 markdown", "发md", "发 .md", "发群", "放群", "请发", "传给", "传我", "给我",
		"生成", "导出", "下载", "保存", "输出", "整理成", "整理一份", "做成", "做一份", "做份", "做一个", "做个",
		"写一份", "写份", "写个", "出一份", "出份", "转成", "制作", "打包", "归档", "投递",
		"send", "attach", "export", "download", "generate", "create", "save", "write", "make", "build", "produce", "render",
	}
	requested := false
	for _, clause := range strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune("，。！？；,!?;\n", r)
	}) {
		hasFile := hasFileKeyword(clause)
		hasRequest := false
		for _, word := range requestWords {
			hasRequest = hasRequest || strings.Contains(clause, word)
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
		(legacyMode && len(state.attempts) != 0) ||
		(state.names[name] && !requestsMultipleVersions(p.MessageText)) {
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
