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
	"unicode"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
)

type imArtifactCollector interface {
	SnapshotIM(context.Context, string, string, uint64, uint64, func(string) bool) (*appservice.IMArtifactSnapshot, error)
	CollectIM(context.Context, *appservice.IMArtifactSnapshot, string, func(string, bool) appservice.IMArtifactSelection) (types.MessageArtifacts, error)
	ValidateIM(context.Context, *appservice.IMArtifactSnapshot, bool) error
}

type imArtifactTurnKey struct{}

var imSandboxLink = regexp.MustCompile(`\[([^\]\r\n]+)\]\(sandbox:([^\s)]+)\)`)

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
	if req == nil || req.channel == nil || req.channel.Platform != "octo" ||
		req.scope == nil || !req.scope.AllowFileDelivery || req.session == nil ||
		req.agent == nil || req.msg == nil ||
		!octobusiness.WantsFileDelivery(req.msg.Content) {
		return ctx
	}
	if req.msg.ChatType != ChatTypeDirect && !octobusiness.WantsExplicitFileSend(req.msg.Content) {
		return ctx
	}
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || grant.SessionID != req.session.ID || grant.TenantID != req.session.TenantID {
		// Text delivery does not require a sandbox; do not turn a successful
		// text export into a misleading sandbox error when no grant exists.
		return ctx
	}
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
			return octobusiness.MentionsArtifactName(req.msg.Content, relative) || octobusiness.MentionsArtifactName(req.msg.Content, path.Base(relative))
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
	if !ok || t == nil || msg == nil || ctx.Err() != nil {
		return answer
	}
	if octobusiness.HasDeliveredFile(ctx) {
		return answer
	}
	if t.startErr != nil || t.authorize(ctx) != nil {
		return answer + "\n\n文件发送未完成：无法安全读取本次会话的产物。"
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
		if ambiguous && !explicit {
			return appservice.IMArtifactNotSelected
		}
		if explicit || (!ambiguous && octobusiness.MentionsArtifactName(text, name)) {
			return appservice.IMArtifactCurrentTurn
		}
		if ambiguous || imTemporaryArtifact(relative) || octobusiness.HasNamedArtifact(text) ||
			!candidates[name] || !octobusiness.MatchesRequestedArtifactType(text, name) {
			return appservice.IMArtifactNotSelected
		}
		return appservice.IMArtifactCurrentTurn
	}
	// The sandbox may not exist until the first command. CollectIM checks the
	// unpinned case separately; the strict extra check is for actual delivery.
	artifacts, collectErr := t.collector.CollectIM(ctx, t.snapshot, msg.ID, selected)
	if len(artifacts) == 0 {
		if collectErr != nil {
			return answer + "\n\n文件未能收集，请检查文件大小或沙箱状态后重试。"
		}
		var names []string
		for name := range candidates {
			names = append(names, name)
		}
		sort.Strings(names)
		answer = renderDeliveredArtifactNames(answer, names)
		if len(names) > 0 {
			return answer + "\n\n未发送文件：未找到可确认属于本轮的目标文件。请重新生成并指定文件名，例如：" + strings.Join(names, "、") + "。"
		}
		return answer + "\n\n未发送文件：未找到可确认属于本轮的目标文件，请重新生成并指定文件名。"
	}
	callCtx, err := octobusiness.WithCurrentFileDeliveryAuthority(callCtx, true)
	if err != nil {
		return answer + "\n\n文件发送未完成：当前会话的授权已变化。"
	}
	if len(artifacts) > 0 {
		msg.Artifacts = append(msg.Artifacts, artifacts...)
	}
	sender, ok := t.req.adapter.(generatedFileSender)
	if !ok {
		return answer + "\n\n文件发送未完成：当前渠道不支持附件投递。"
	}
	var sent, failed, unknown []string
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
		if err := sender.SendGeneratedFile(callCtx, t.req.msg, artifact.FileName, data, contentType); err != nil {
			if outcome, ok := err.(interface{ DeliveryOutcome() string }); ok && outcome.DeliveryOutcome() == "failed" {
				failed = append(failed, artifact.FileName)
			} else {
				unknown = append(unknown, artifact.FileName)
			}
			continue
		}
		sent = append(sent, artifact.FileName)
	}
	if len(sent) > 0 {
		answer = renderDeliveredArtifactNames(answer, sent)
		answer += "\n\n已发送文件：" + strings.Join(sent, "、") + "。"
	}
	if len(failed) > 0 {
		answer += "\n\n文件发送失败：" + strings.Join(failed, "、") + "。"
	}
	if len(unknown) > 0 {
		answer += "\n\n文件发送状态未知，请先检查会话再重试：" + strings.Join(unknown, "、") + "。"
	}
	if collectErr != nil {
		answer += "\n\n部分文件未能收集，请检查文件大小或沙箱状态后重试。"
	}
	return answer
}

// Only an exact, path-free sandbox link can nominate a current-turn output.
// The collector still resolves it inside the authorized session's output tree.
func imArtifactLinkNames(answer string) map[string]bool {
	names := map[string]bool{}
	for _, parts := range imSandboxLink.FindAllStringSubmatch(answer, -1) {
		name, err := url.PathUnescape(parts[2])
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
	allowed := make(map[string]bool, len(sent))
	for _, name := range sent {
		allowed[name] = true
	}
	return imSandboxLink.ReplaceAllStringFunc(answer, func(match string) string {
		parts := imSandboxLink.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		name, err := url.PathUnescape(parts[2])
		if err != nil || !allowed[name] {
			return match
		}
		return parts[1]
	})
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
	defer reader.Close()
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
