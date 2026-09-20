package im

import (
	"context"

	"github.com/Tencent/WeKnora/internal/octobusiness"
)

type reportSender interface {
	ReportContext(context.Context, octobusiness.ReportSchedule) (context.Context, error)
	DeliverReport(context.Context, octobusiness.ReportSchedule, *octobusiness.Report, *octobusiness.ReportArtifact) error
}

func (s *Service) ConfigureOctoReports(business *octobusiness.Service) {
	lookup := func(schedule octobusiness.ReportSchedule) (reportSender, error) {
		a, ch, ok := s.GetChannelAdapter(schedule.ChannelID)
		if !ok || ch.TenantID != schedule.TenantID || ch.Platform != "octo" || !ch.Enabled {
			return nil, ErrScopeDenied
		}
		r, ok := a.(reportSender)
		if !ok {
			return nil, ErrScopeDenied
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
}
