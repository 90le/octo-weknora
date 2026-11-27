package im

import (
	"context"

	"github.com/Tencent/WeKnora/internal/octobusiness"
)

type reportSender interface {
	ReportContext(context.Context, octobusiness.ReportSchedule) (context.Context, error)
	DeliverReport(context.Context, octobusiness.ReportSchedule, *octobusiness.Report, *octobusiness.ReportArtifact) error
}

type multiTargetReportSender interface {
	ListBotDeliveryTargets(context.Context, uint64) (*octobusiness.BotDeliveryTargetsResponse, error)
	VerifyBotFriend(context.Context, uint64, string) (*octobusiness.BotFriendVerifyResult, error)
	DeliverReportToTarget(context.Context, uint64, octobusiness.DeliveryTarget, string, *octobusiness.Report, *octobusiness.ReportArtifact) (string, error)
	ResolveUserNames(context.Context, uint64, []string) map[string]string
}

type manualReplySender interface {
	ManualReplyRecipientName(context.Context, octobusiness.ManualReplyTarget) (string, error)
	DeliverManualReply(context.Context, octobusiness.ManualReplyTarget, string, string) (string, error)
}

func (s *Service) ConfigureOctoReports(business *octobusiness.Service) {
	lookup := func(schedule octobusiness.ReportSchedule) (reportSender, error) {
		a, ch, ok := s.GetChannelAdapter(schedule.ChannelID)
		if !ok || ch.TenantID != schedule.TenantID || ch.Platform != "octo" || !ch.Enabled {
			return nil, octobusiness.ReportAccessError("channel")
		}
		r, ok := a.(reportSender)
		if !ok {
			return nil, octobusiness.ReportAccessError("channel")
		}
		return r, nil
	}
	business.ReportPrincipal = func(ctx context.Context, schedule octobusiness.ReportSchedule) (context.Context, error) {
		r, err := lookup(schedule)
		if err != nil {
			return nil, err
		}
		return r.ReportContext(ctx, schedule)
	}
	business.ReportDelivery = func(ctx context.Context, schedule octobusiness.ReportSchedule, report *octobusiness.Report, file *octobusiness.ReportArtifact) error {
		r, err := lookup(schedule)
		if err != nil {
			return err
		}
		return r.DeliverReport(ctx, schedule, report, file)
	}
	lookupMulti := func(tenant uint64, channelID string) (multiTargetReportSender, error) {
		a, ch, ok := s.GetChannelAdapter(channelID)
		if !ok || ch.TenantID != tenant || ch.Platform != "octo" || !ch.Enabled {
			return nil, octobusiness.ReportAccessError("channel")
		}
		m, ok := a.(multiTargetReportSender)
		if !ok {
			return nil, octobusiness.ReportAccessError("channel")
		}
		return m, nil
	}
	business.BotDeliveryTargets = func(ctx context.Context, tenant uint64, channelID string) (*octobusiness.BotDeliveryTargetsResponse, error) {
		m, err := lookupMulti(tenant, channelID)
		if err != nil {
			return nil, err
		}
		return m.ListBotDeliveryTargets(ctx, tenant)
	}
	business.BotVerifyFriend = func(ctx context.Context, tenant uint64, channelID, uid string) (*octobusiness.BotFriendVerifyResult, error) {
		m, err := lookupMulti(tenant, channelID)
		if err != nil {
			return nil, err
		}
		return m.VerifyBotFriend(ctx, tenant, uid)
	}
	business.MultiTargetDelivery = func(ctx context.Context, tenant uint64, channelID string, target octobusiness.DeliveryTarget, runKey string, report *octobusiness.Report, file *octobusiness.ReportArtifact) (string, error) {
		m, err := lookupMulti(tenant, channelID)
		if err != nil {
			return "", err
		}
		return m.DeliverReportToTarget(ctx, tenant, target, runKey, report, file)
	}
	business.ResolveOctoUserNames = func(ctx context.Context, tenant uint64, channelID string, uids []string) map[string]string {
		if len(uids) == 0 {
			return nil
		}
		if channelID != "" {
			if m, err := lookupMulti(tenant, channelID); err == nil {
				return m.ResolveUserNames(ctx, tenant, uids)
			}
		}
		channels, err := s.ListChannelsByTenant(ctx, tenant)
		if err != nil {
			return nil
		}
		for _, ch := range channels {
			if ch.Platform != "octo" || !ch.Enabled {
				continue
			}
			if m, err := lookupMulti(tenant, ch.ID); err == nil {
				return m.ResolveUserNames(ctx, tenant, uids)
			}
		}
		return nil
	}
	findManualSender := func(target octobusiness.ManualReplyTarget) (manualReplySender, error) {
		a, ch, ok := s.GetChannelAdapter(target.ChannelID)
		if !ok || ch.TenantID != target.TenantID || ch.Platform != "octo" || !ch.Enabled {
			return nil, octobusiness.ErrDenied
		}
		sender, ok := a.(manualReplySender)
		if !ok {
			return nil, octobusiness.ErrDenied
		}
		return sender, nil
	}
	business.ManualReplyRecipientName = func(
		ctx context.Context, target octobusiness.ManualReplyTarget,
	) (string, error) {
		sender, err := findManualSender(target)
		if err != nil {
			return "", err
		}
		return sender.ManualReplyRecipientName(ctx, target)
	}
	business.ManualReplyDelivery = func(
		ctx context.Context, target octobusiness.ManualReplyTarget, id, body string,
	) (string, error) {
		sender, err := findManualSender(target)
		if err != nil {
			return "", err
		}
		return sender.DeliverManualReply(ctx, target, id, body)
	}
}
