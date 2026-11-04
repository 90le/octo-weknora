package im

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
)

type imArtifactCollector interface {
	SnapshotIM(
		context.Context,
		string,
		string,
		uint64,
		uint64,
		func(string) bool,
	) (*appservice.IMArtifactSnapshot, error)
	CollectIM(
		context.Context,
		*appservice.IMArtifactSnapshot,
		string,
		func(string, bool) appservice.IMArtifactSelection,
	) (types.MessageArtifacts, error)
	ValidateIM(context.Context, *appservice.IMArtifactSnapshot, bool) error
}

type imArtifactTurnKey struct{}
type imOctoArtifactGuardKey struct{}
type imFileRequestKey struct{}

type imDeliveredTextCollector interface {
	PersistDeliveredText(
		context.Context, string, string, uint64, string, []byte, func(context.Context) error,
	) (types.MessageArtifact, error)
	DiscardDeliveredText(context.Context, string, types.MessageArtifact) error
}

// Save only files actually delivered in this turn, after rechecking the same
// principal and scope. Text delivery also works when the sandbox is unavailable.
func (s *Service) persistIMDeliveredText(ctx context.Context, msg *types.Message) []types.MessageArtifact {
	if msg == nil || msg.ID == "" || msg.SessionID == "" {
		return nil
	}
	collector, ok := s.artifactCollector.(imDeliveredTextCollector)
	if !ok {
		return nil
	}
	principal, ok := octobusiness.PrincipalFromContext(ctx)
	sessionID, sessionOK := types.SessionIDFromContext(ctx)
	if !ok || !sessionOK || sessionID != msg.SessionID || principal.TenantID == 0 {
		return nil
	}
	files, err := octobusiness.DeliveredTextFiles(ctx)
	if err != nil {
		return nil
	}
	var persisted []types.MessageArtifact
	for _, file := range files {
		name, content := file.Name, file.Content
		authorize := func(checkCtx context.Context) error {
			current, err := octobusiness.DeliveredTextFiles(checkCtx)
			if err != nil {
				return err
			}
			for _, item := range current {
				if item.Name == name && item.Content == content {
					return nil
				}
			}
			return ErrScopeDenied
		}
		artifact, err := collector.PersistDeliveredText(
			ctx, msg.SessionID, msg.ID, principal.TenantID, name, []byte(content), authorize,
		)
		if err != nil {
			logger.Warnf(ctx, "[IM] delivered text artifact was not recorded")
			continue
		}
		msg.Artifacts = append(msg.Artifacts, artifact)
		persisted = append(persisted, artifact)
	}
	return persisted
}

// A failed database write can have committed despite the error. Re-read the
// message before discarding any unreferenced objects; an uncertain read keeps
// them intact for resource reconciliation.
func (s *Service) cleanupIMDeliveredText(ctx context.Context, msg *types.Message, files []types.MessageArtifact) {
	collector, ok := s.artifactCollector.(imDeliveredTextCollector)
	if !ok || s.messageService == nil || len(files) == 0 {
		return
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	stored, err := s.messageService.GetMessage(readCtx, msg.SessionID, msg.ID)
	if err != nil || stored == nil {
		logger.Warnf(ctx, "[IM] delivered artifact persistence outcome uncertain")
		return
	}
	for _, file := range files {
		found := false
		for _, item := range stored.Artifacts {
			if item.URL == file.URL {
				found = true
				break
			}
		}
		if !found && collector.DiscardDeliveredText(readCtx, msg.ID, file) != nil {
			logger.Warnf(ctx, "[IM] unreferenced delivered artifact cleanup failed")
		}
	}
}

var (
	imSandboxLink        = regexp.MustCompile(`(?i)!?\[([^\]\r\n]+)\]\(sandbox:([^\r\n)]+)\)`)
	imBareSandboxLink    = regexp.MustCompile(`(?i)sandbox:[^\s<>()]+`)
	imAnswerFileName     = regexp.MustCompile(`[\p{L}\p{N}_-][\p{L}\p{N}_.-]*\.[\p{L}\p{N}]{1,16}`)
	imPPTRequest         = regexp.MustCompile(`(?i)\bppt\b|幻灯片`)
	imCompoundClaim      = regexp.MustCompile(`(?:已经|已)(?:成功)?(?:生成|制作|完成)(?:并|后)?(?:已|成功)?(?:发送|投递)(?:了)?(?:文件|附件)?`)
	imGenerationClaim    = regexp.MustCompile(`(?:(?:已经|已)(?:成功)?(?:生成|制作|完成|做好)|(?:生成|制作)(?:成功|完成)|成功(?:生成|制作))`)
	imDownloadBeforeLink = regexp.MustCompile(`(?:请)?点击(?:下方|这里|此处|以下)?(?:链接)?下载[：: ]*$`)
	imDeliveryClaim      = regexp.MustCompile(`(?:文件|附件)(?:(?:已经|已)(?:成功)?(?:发送|投递)(?:成功|完成)?|` +
		`(?:发送|投递)(?:成功|完成)|成功(?:发送|投递))|(?:(?:已经|已)(?:成功)?|成功)(?:发送|投递)(?:了)?(?:文件|附件)`)
)

type imArtifactTurn struct {
	collector   imArtifactCollector
	snapshot    *appservice.IMArtifactSnapshot
	req         *qaRequest
	fingerprint string
	startErr    error
}

// ConfigureArtifactCollector is called before channels start. The collector is
// shared with Web; it is only used for explicitly authorized Octo requests.
func (s *Service) ConfigureArtifactCollector(collector *appservice.ArtifactCollector) {
	if collector != nil {
		s.artifactCollector = collector
	}
}

func (s *Service) beginIMArtifactTurn(ctx context.Context, req *qaRequest) context.Context {
	if req == nil || req.channel == nil || req.channel.Platform != "octo" {
		return ctx
	}
	ctx = context.WithValue(ctx, imOctoArtifactGuardKey{}, true)
	if req.msg == nil {
		return ctx
	}
	// The shared request classifier recognizes pptx but not the common “PPT”
	// and “幻灯片” names. Normalize only for request admission.
	requestText := imPPTRequest.ReplaceAllString(req.msg.Content, "pptx")
	ctx = context.WithValue(ctx, imFileRequestKey{}, octobusiness.WantsFileDelivery(requestText))
	if req.scope == nil || !req.scope.AllowFileDelivery || req.session == nil ||
		req.agent == nil || req.msg == nil ||
		!octobusiness.WantsFileDelivery(requestText) {
		return ctx
	}
	if req.msg.ChatType != ChatTypeDirect && !octobusiness.WantsExplicitFileSend(requestText) {
		return ctx
	}
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || grant.SessionID != req.session.ID || grant.TenantID != req.session.TenantID {
		// Text delivery does not require a sandbox; do not turn a successful
		// text export into a misleading sandbox error when no grant exists.
		return ctx
	}
	// Tell the model how the existing channel collector works; it cannot send a
	// binary through the text-only delivery tool or a sandbox URL.
	instruction := "文件请求：使用已授权的工具实际生成文件，完成文件放在 /workspace/output/；" +
		"在最终答复中列出准确文件名。系统将在本轮按授权收集并投递，sandbox: 链接不是 Octo 下载地址。" +
		"不得把计划、命令或未确认的投递说成成功；如生成失败请说明实际工具结果。"
	prior, _ := ctx.Value(executionContextKey{}).(string)
	ctx = context.WithValue(ctx, executionContextKey{}, strings.TrimSpace(prior+"\n\n"+instruction))
	t := &imArtifactTurn{collector: s.artifactCollector, req: req, fingerprint: scopeFingerprint(req.scope)}
	if t.collector == nil {
		t.startErr = errors.New("artifact collection unavailable")
		return context.WithValue(ctx, imArtifactTurnKey{}, t)
	}
	if err := t.authorize(ctx); err != nil {
		t.startErr = err
		return context.WithValue(ctx, imArtifactTurnKey{}, t)
	}
	t.snapshot, t.startErr = t.collector.SnapshotIM(
		ctx, req.session.ID, grant.ConfigID, req.session.TenantID, grant.TenantID,
		func(relative string) bool {
			return octobusiness.MentionsArtifactName(req.msg.Content, relative) ||
				octobusiness.MentionsArtifactName(req.msg.Content, path.Base(relative))
		},
	)
	return context.WithValue(ctx, imArtifactTurnKey{}, t)
}

func (t *imArtifactTurn) authorize(ctx context.Context) error {
	current, err := authorizeExecution(ctx, t.req.adapter, t.req.channel, t.req.msg)
	if err != nil || current == nil || !current.AllowFileDelivery ||
		scopeFingerprint(current) != t.fingerprint {
		return ErrScopeDenied
	}
	return nil
}

// finishIMArtifactTurn delivers selected files verified as changed during this turn.
func (s *Service) finishIMArtifactTurn(ctx context.Context, msg *types.Message, answer string) string {
	t, ok := ctx.Value(imArtifactTurnKey{}).(*imArtifactTurn)
	if ctx.Err() != nil {
		return answer
	}
	if !ok || t == nil || msg == nil {
		if guarded, _ := ctx.Value(imOctoArtifactGuardKey{}).(bool); !guarded {
			return answer
		}
		if octobusiness.HasDeliveredFile(ctx) {
			return renderConfirmedTextDelivery(ctx, answer)
		}
		requested, _ := ctx.Value(imFileRequestKey{}).(bool)
		if imSandboxLink.MatchString(answer) || imBareSandboxLink.MatchString(answer) ||
			(requested && (imDeliveryClaim.MatchString(answer) || imGenerationClaim.MatchString(answer))) {
			return imUnconfirmedArtifactAnswer(answer) + "\n\n文件未确认生成或发送，请检查工具执行结果后重试。"
		}
		return answer
	}
	if octobusiness.HasDeliveredFile(ctx) {
		// Do not repeat an already attempted delivery.
		return renderConfirmedTextDelivery(ctx, answer)
	}
	if t.startErr != nil || t.authorize(ctx) != nil {
		return imUnconfirmedArtifactAnswer(answer) + "\n\n文件发送未完成：无法安全读取本次会话的产物。"
	}
	callCtx := octobusiness.WithFileDeliveryExtraCheck(ctx, func(checkCtx context.Context) error {
		if err := t.authorize(checkCtx); err != nil {
			return err
		}
		return t.collector.ValidateIM(checkCtx, t.snapshot, false)
	})
	text := t.req.msg.Content
	if principal, ok := octobusiness.PrincipalFromContext(ctx); ok {
		text = principal.MessageText
	}
	candidates := imArtifactLinkNames(answer)
	selected := func(relative string, ambiguous bool) appservice.IMArtifactSelection {
		name := path.Base(relative)
		explicit := octobusiness.MentionsArtifactName(text, relative)
		if ambiguous && !strings.Contains(relative, "/") {
			explicit = octobusiness.MentionsArtifactName(text, "./"+relative)
		}
		if imTemporaryArtifact(relative) {
			// A basename alone must not select a file in a temporary location.
			// Require the actual relative path, including ./ for a root file.
			if strings.Contains(relative, "/") {
				if !explicit {
					return appservice.IMArtifactNotSelected
				}
			} else if !octobusiness.MentionsArtifactName(text, "./"+relative) {
				return appservice.IMArtifactNotSelected
			}
		}
		if ambiguous && !explicit {
			return appservice.IMArtifactNotSelected
		}
		if explicit || (!ambiguous && octobusiness.MentionsArtifactName(text, name)) {
			return appservice.IMArtifactCurrentTurn
		}
		if ambiguous || imTemporaryArtifact(relative) || octobusiness.HasNamedArtifact(text) ||
			(!candidates[name] && !octobusiness.MentionsArtifactName(answer, name)) ||
			!octobusiness.MatchesRequestedArtifactType(text, name) {
			return appservice.IMArtifactNotSelected
		}
		return appservice.IMArtifactCurrentTurn
	}
	// The sandbox may not exist until the first command. CollectIM checks the
	// unpinned case separately; the strict extra check is for actual delivery.
	artifacts, collectErr := t.collector.CollectIM(ctx, t.snapshot, msg.ID, selected)
	if len(artifacts) == 0 {
		if collectErr != nil {
			return imUnconfirmedArtifactAnswer(answer) + "\n\n文件未能收集，请检查文件大小或沙箱状态后重试。"
		}
		var names []string
		for name := range candidates {
			names = append(names, name)
		}
		sort.Strings(names)
		answer = imUnconfirmedArtifactAnswer(answer)
		if len(names) > 0 {
			return answer + "\n\n未发送文件：未找到可确认属于本轮的目标文件。请重新生成并指定文件名，例如：" + strings.Join(names, "、") + "。"
		}
		return answer + "\n\n未发送文件：未找到可确认属于本轮的目标文件，请重新生成并指定文件名。"
	}
	answer = imCompoundClaim.ReplaceAllString(answer, "文件已收集，发送状态待确认")
	answer = imDeliveryClaim.ReplaceAllString(answer, "文件发送状态待确认")
	callCtx, err := octobusiness.WithCurrentFileDeliveryAuthority(callCtx, true)
	if err != nil {
		return renderIMArtifactAnswer(answer, nil, true) + "\n\n文件发送未完成：当前会话的授权已变化。"
	}
	if len(artifacts) > 0 {
		msg.Artifacts = append(msg.Artifacts, artifacts...)
	}
	sender, ok := t.req.adapter.(generatedFileSender)
	if !ok {
		return renderIMArtifactAnswer(answer, nil, true) + "\n\n文件发送未完成：当前渠道不支持附件投递。"
	}
	var sent, sentDisplay, failed, unknown []string
	for _, artifact := range artifacts {
		if t.authorize(callCtx) != nil || octobusiness.ValidateFileDeliveryAuthority(callCtx) != nil {
			failed = append(failed, artifact.FileName)
			continue
		}
		data, err := s.readIMArtifact(callCtx, artifact)
		if err != nil {
			failed = append(failed, artifact.FileName)
			continue
		}
		contentType := mime.TypeByExtension(strings.ToLower(path.Ext(artifact.FileName)))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		deliveredName := artifact.FileName
		var deliveryErr error
		if reporting, ok := sender.(interface {
			SendGeneratedFileWithResult(context.Context, *IncomingMessage, string, []byte, string) (string, error)
		}); ok {
			deliveredName, deliveryErr = reporting.SendGeneratedFileWithResult(
				callCtx, t.req.msg, artifact.FileName, data, contentType,
			)
		} else {
			deliveryErr = sender.SendGeneratedFile(callCtx, t.req.msg, artifact.FileName, data, contentType)
		}
		if err := deliveryErr; err != nil {
			if outcome, ok := err.(interface{ DeliveryOutcome() string }); ok && outcome.DeliveryOutcome() == "failed" {
				failed = append(failed, artifact.FileName)
			} else {
				unknown = append(unknown, artifact.FileName)
			}
			continue
		}
		sent = append(sent, artifact.FileName)
		if deliveredName != "" && deliveredName != artifact.FileName {
			sentDisplay = append(sentDisplay, deliveredName+"（包含 "+artifact.FileName+"）")
		} else {
			sentDisplay = append(sentDisplay, artifact.FileName)
		}
	}
	if len(failed) > 0 || len(unknown) > 0 || collectErr != nil {
		answer = imDeliveryClaim.ReplaceAllString(answer, "文件发送状态待确认")
	}
	if len(sent) > 0 {
		answer = renderDeliveredArtifactNames(answer, sent)
		answer += "\n\n已发送文件：" + strings.Join(sentDisplay, "、") + "。"
		for _, display := range sentDisplay {
			if strings.Contains(display, "（包含 ") &&
				strings.HasSuffix(strings.ToLower(strings.SplitN(display, "（", 2)[0]), ".zip") {
				answer += "\n请下载 ZIP 压缩包并解压后查看其中的文件。"
				break
			}
		}
	}
	if len(failed) > 0 {
		answer = renderIMArtifactAnswer(answer, failed, false)
		answer += "\n\n文件发送失败：" + strings.Join(failed, "、") + "。"
	}
	if len(unknown) > 0 {
		answer = renderIMArtifactAnswer(answer, unknown, false)
		answer += "\n\n文件发送状态未知，请先检查会话再重试：" + strings.Join(unknown, "、") + "。"
	}
	if collectErr != nil {
		answer += "\n\n部分文件未能收集，请检查文件大小或沙箱状态后重试。"
	}
	return renderIMArtifactAnswer(answer, nil, false)
}

// One successful text delivery cannot confirm other file names in the answer.
func renderConfirmedTextDelivery(ctx context.Context, answer string) string {
	files, err := octobusiness.DeliveredTextFiles(ctx)
	remaining := answer
	for _, file := range files {
		remaining = strings.ReplaceAll(remaining, file.Name, "")
	}
	other := imSandboxLink.MatchString(remaining) || imBareSandboxLink.MatchString(remaining) ||
		imAnswerFileName.MatchString(remaining)
	if err != nil || other {
		return imUnconfirmedArtifactAnswer(answer) + "\n\n其他文件是否已生成或发送，请以实际投递结果为准。"
	}
	return renderIMArtifactAnswer(answer, nil, false)
}

func imUnconfirmedArtifactAnswer(answer string) string {
	answer = imCompoundClaim.ReplaceAllString(answer, "生成和发送状态未确认")
	return renderIMArtifactAnswer(imGenerationClaim.ReplaceAllString(answer, "生成状态未确认"), nil, true)
}

// Only an exact, path-free sandbox link can nominate a current-turn output.
// The collector still resolves it inside the authorized session's output tree.
func imArtifactLinkNames(answer string) map[string]bool {
	names := map[string]bool{}
	for _, parts := range imSandboxLink.FindAllStringSubmatch(answer, -1) {
		name, err := url.PathUnescape(strings.TrimSpace(parts[2]))
		if err != nil || name == "" || name == "." || name == ".." ||
			name != path.Base(name) || len(name) > 255 || strings.ContainsAny(name, "\\/") {
			continue
		}
		unsafe := false
		for _, r := range name {
			unsafe = unsafe || unicode.IsControl(r)
		}
		if !unsafe {
			names[name] = true
		}
	}
	return names
}

func imTemporaryArtifact(relative string) bool {
	for _, component := range strings.Split(strings.ToLower(relative), "/") {
		if component == "tmp" || component == "temp" || component == "scratch" ||
			component == "intermediate" || strings.HasPrefix(component, ".") ||
			strings.HasPrefix(component, "~") || strings.HasSuffix(component, ".tmp") ||
			strings.HasSuffix(component, ".part") {
			return true
		}
	}
	return false
}

// A sandbox: URL is meaningful in Web but not in Octo. Once the actual file
// was delivered, show its label as text rather than a broken download link.
func renderDeliveredArtifactNames(answer string, sent []string) string {
	return renderIMArtifactAnswer(answer, sent, false)
}

// A nil name list removes all sandbox links without claiming they were sent.
func renderIMArtifactAnswer(answer string, names []string, uncertain bool) string {
	if uncertain {
		answer = imDeliveryClaim.ReplaceAllString(answer, "文件发送状态待确认")
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	var result strings.Builder
	last := 0
	for _, indexes := range imSandboxLink.FindAllStringSubmatchIndex(answer, -1) {
		name, err := url.PathUnescape(strings.TrimSpace(answer[indexes[4]:indexes[5]]))
		if err != nil || (names != nil && !allowed[name]) {
			continue
		}
		prefix := answer[last:indexes[0]]
		prefix = imDownloadBeforeLink.ReplaceAllString(prefix, "")
		result.WriteString(prefix)
		label := answer[indexes[2]:indexes[3]]
		if strings.Contains(label, "下载") || strings.Contains(label, "点击") {
			label = name
		}
		result.WriteString(label)
		last = indexes[1]
	}
	result.WriteString(answer[last:])
	if names == nil {
		return imBareSandboxLink.ReplaceAllString(result.String(), "文件地址不可用")
	}
	return result.String()
}

func (s *Service) readIMArtifact(ctx context.Context, artifact types.MessageArtifact) ([]byte, error) {
	const maxBytes int64 = 20 << 20
	if s.defaultFileSvc == nil || artifact.FileSize <= 0 || artifact.FileSize > maxBytes ||
		artifact.ContentHash == "" {
		return nil, errors.New("invalid captured artifact")
	}
	reader, err := s.defaultFileSvc.GetFile(ctx, artifact.URL)
	if err != nil || reader == nil {
		return nil, errors.New("captured artifact unavailable")
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil || int64(len(data)) != artifact.FileSize {
		return nil, errors.New("captured artifact size changed")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != artifact.ContentHash {
		return nil, fmt.Errorf("captured artifact hash mismatch")
	}
	return data, nil
}
