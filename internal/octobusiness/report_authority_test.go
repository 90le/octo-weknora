package octobusiness

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReportAuthorityRejectsPartialKBAndChildRevocation(t *testing.T) {
	p := testPrincipal()
	p.KnowledgeBaseIDs = []string{"kb-a", "kb-b"}
	fresh := p
	p.Validate = func(context.Context) (Principal, error) { return fresh, nil }
	ctx := WithReportAuthority(WithPrincipal(context.Background(), p), p)
	require.NoError(t, ValidateReportAuthority(ctx))
	fresh.KnowledgeBaseIDs = []string{"kb-b"}
	require.ErrorIs(t, ValidateReportAuthority(ctx), ErrDenied, "remaining valid KB cannot authorize an old report containing a revoked KB")
	fresh = p
	fresh.ReadIssueScopeIDs = []string{"child"}
	fresh.ReadIssueKnowledgeBaseIDs = []string{"kb-child"}
	p = fresh
	p.Validate = func(context.Context) (Principal, error) { return fresh, nil }
	ctx = WithReportAuthority(WithPrincipal(context.Background(), p), p)
	fresh.ReadIssueScopeIDs = nil
	require.ErrorIs(t, ValidateReportAuthority(ctx), ErrDenied)
	require.NoError(t, ValidateReportAuthority(context.Background()), "ordinary non-report content has its own native authorization")
}
func TestReportArtifactsDisplayActualTimeRange(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)
	report := &Report{From: &from, To: &to, GeneratedAt: to, Items: []Issue{{ID: "one"}}}
	for _, format := range []string{"html", "markdown", "csv"} {
		file, err := ReportFile(report, format)
		require.NoError(t, err)
		require.Contains(t, file.Content, from.Format(time.RFC3339))
		require.Contains(t, file.Content, to.Format(time.RFC3339))
	}
}
