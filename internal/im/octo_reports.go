package im

import (
	"context"

	"github.com/Tencent/WeKnora/internal/octobusiness"
)

type reportSender interface {
	ReportContext(context.Context, octobusiness.ReportSchedule) (context.Context, error)
	DeliverReport(context.Context, octobusiness.ReportSchedule, *octobusiness.Report, *octobusiness.ReportArtifact) error
}

type manualReplySender interface {
	CheckManualReply(context.Context, octobusiness.ManualReplyTarget) error
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
	business.ManualReplyCheck = func(ctx context.Context, target octobusiness.ManualReplyTarget) error {
		sender, err := findManualSender(target)
		if err != nil {
			return err
		}
		return sender.CheckManualReply(ctx, target)
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
