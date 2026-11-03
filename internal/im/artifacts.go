package im

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
)

type imArtifactCollector interface {
	SnapshotIM(context.Context, string, string, uint64, uint64) (*appservice.IMArtifactSnapshot, error)
	CollectIM(context.Context, *appservice.IMArtifactSnapshot, string) (types.MessageArtifacts, error)
	ValidateIM(context.Context, *appservice.IMArtifactSnapshot, bool) error
}

type imArtifactTurnKey struct{}

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
	t := &imArtifactTurn{collector: s.artifactCollector, req: req, fingerprint: scopeFingerprint(req.scope)}
	if t.collector == nil {
		t.startErr = errors.New("artifact collection unavailable")
		return context.WithValue(ctx, imArtifactTurnKey{}, t)
	}
	if err := t.authorize(ctx); err != nil {
		t.startErr = err
		return context.WithValue(ctx, imArtifactTurnKey{}, t)
	}
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || grant.SessionID != req.session.ID || grant.TenantID != req.session.TenantID {
		t.startErr = errors.New("sandbox grant unavailable")
		return context.WithValue(ctx, imArtifactTurnKey{}, t)
	}
	t.snapshot, t.startErr = t.collector.SnapshotIM(
		ctx, req.session.ID, grant.ConfigID, req.session.TenantID, grant.TenantID,
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

// finishIMArtifactTurn delivers only files captured from this turn's bounded,
// session-owned sandbox. It never accepts model-selected paths or recipients.
func (s *Service) finishIMArtifactTurn(ctx context.Context, msg *types.Message, answer string) string {
	t, ok := ctx.Value(imArtifactTurnKey{}).(*imArtifactTurn)
	if !ok || t == nil || msg == nil || ctx.Err() != nil {
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
	// The sandbox may not exist until the first command. CollectIM checks the
	// unpinned case separately; the strict extra check is for actual delivery.
	artifacts, collectErr := t.collector.CollectIM(ctx, t.snapshot, msg.ID)
	if len(artifacts) == 0 {
		if collectErr != nil {
			return answer + "\n\n文件未能收集，请检查文件大小或沙箱状态后重试。"
		}
		return answer
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
	var sent, failed []string
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
			failed = append(failed, artifact.FileName)
			continue
		}
		sent = append(sent, artifact.FileName)
	}
	if len(sent) > 0 {
		answer += "\n\n已发送文件：" + strings.Join(sent, "、") + "。"
	}
	if len(failed) > 0 {
		answer += "\n\n文件发送失败或状态未知：" + strings.Join(failed, "、") + "。"
	}
	if collectErr != nil {
		answer += "\n\n部分文件未能收集，请检查文件大小或沙箱状态后重试。"
	}
	return answer
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
