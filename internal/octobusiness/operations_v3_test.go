package octobusiness

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func seedV3ScopesAndInbox(t *testing.T, s *Service) {
	t.Helper()
	for _, name := range []string{"000037_octo_scopes", "000043_octo_knowledge_grants", "000056_octo_member_questions"} {
		sql, err := os.ReadFile("../../migrations/sqlite/" + name + ".up.sql")
		require.NoError(t, err)
		require.NoError(t, s.db.Exec(string(sql)).Error)
	}
	require.NoError(t, s.db.Exec(`INSERT INTO octo_scopes (id,tenant_id,account_id,group_id,subarea_id,display_name,name_source,sync_status,verified_at,created_at,updated_at)
		VALUES ('scope-g1',1,'bot','g1','','AN-业问小灶版内测','octo','verified',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`).Error)
	require.NoError(t, s.db.Exec(`INSERT INTO octo_scope_bindings (tenant_id,scope_id,knowledge_base_id,created_at) VALUES (1,'scope-g1','kb-a',CURRENT_TIMESTAMP)`).Error)
	// Group question + Private DM question (same wording, must stay isolated!) + Failed/Interrupted turn
	require.NoError(t, s.db.Exec(`INSERT INTO octo_message_inbox (tenant_id,channel_id,message_id,authority,input,reply,state,created_at,updated_at) VALUES
		(1,'ch-1','msg-group-1','{"AccountID":"bot","ScopeID":"scope-g1","KnowledgeBaseIDs":["kb-a"]}','{"MessageType":"text","ChatType":"group","UserID":"u1","UserName":"周凯","Content":"支持小红书聚光吗？","Extra":{"octo_group_id":"g1","octo_message_id":"101"}}','未找到相关说明。','delivered',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
		(1,'ch-1','msg-dm-1','{"AccountID":"bot","ScopeID":"","KnowledgeBaseIDs":["kb-a"]}','{"MessageType":"text","ChatType":"direct","UserID":"u2","UserName":"林敏","Content":"支持小红书聚光吗？","Extra":{"octo_message_id":"102"}}','未找到相关说明。','delivered',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
		(1,'ch-1','msg-fail-1','{"AccountID":"bot","ScopeID":"scope-g1","KnowledgeBaseIDs":["kb-a"]}','{"MessageType":"text","ChatType":"group","UserID":"u3","UserName":"陈宇","Content":"CBP 标签明细？","Extra":{"octo_group_id":"g1","octo_message_id":"103"}}','','failed',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`).Error)
	require.NoError(t, s.db.Exec(`INSERT INTO octo_quality_reviews (tenant_id,channel_id,scope_id,message_id,library_id,state,suggestion,finding,updated_at) VALUES
		(1,'ch-1','scope-g1','msg-group-1','kb-a','done','process','{"category":"missing","title":"缺少聚光说明","reason":"现有文档未覆盖","suggestion":"补充聚光能力页"}',CURRENT_TIMESTAMP),
		(1,'ch-1','scope-g1','msg-fail-1','kb-a','failed','','{}',CURRENT_TIMESTAMP)`).Error)
}

func TestQualityV3CrossScopeAndPrivateDMIsolation(t *testing.T) {
	s := testService(t)
	seedV3ScopesAndInbox(t, s)
	ctx := consoleTestContext()

	page, err := s.queryQualityV3(ctx, "", qualityFilterV3{IncludeDM: true})
	require.NoError(t, err)
	require.Equal(t, 3, page.Total)

	var grpKey, dmKey string
	for _, r := range page.Rows {
		if r.ID == "msg-group-1" {
			grpKey = r.GroupKey
			require.Equal(t, "group", r.SourceType)
		}
		if r.ID == "msg-dm-1" {
			dmKey = r.GroupKey
			require.Equal(t, "dm", r.SourceType)
			require.True(t, strings.HasPrefix(r.GroupKey, "dm:"))
		}
		if r.ID == "msg-fail-1" {
			require.True(t, r.Interrupted)
		}
	}
	require.NotEmpty(t, grpKey)
	require.NotEmpty(t, dmKey)
	require.NotEqual(t, grpKey, dmKey, "private DM must not merge into group question group")

	// Filter only DM
	dmOnly, err := s.queryQualityV3(ctx, "", qualityFilterV3{SourceType: "dm", IncludeDM: true})
	require.NoError(t, err)
	require.Equal(t, 1, dmOnly.Total)
	require.Equal(t, "msg-dm-1", dmOnly.Rows[0].ID)

	// Retry failed AI reviews
	retried, err := s.retryFailedQualityReviews(ctx, "", "")
	require.NoError(t, err)
	require.EqualValues(t, 1, retried)
}

func TestWorkbenchSummaryAndOneClickConvert(t *testing.T) {
	s := testService(t)
	seedV3ScopesAndInbox(t, s)
	ctx := consoleTestContext()

	sum, err := s.workbenchSummary(ctx, "", qualityFilterV3{IncludeDM: true})
	require.NoError(t, err)
	require.Equal(t, 1, sum.Alerts.InterruptedCount)
	require.Equal(t, 1, sum.Alerts.FailedAIReviews)
	require.GreaterOrEqual(t, len(sum.ActionStream), 1)
	require.GreaterOrEqual(t, len(sum.KBHealth), 2)

	// 1-click convert AI-reviewed sample straight to Issue
	iss, err := s.convertQualityToIssue(ctx, "msg-group-1", convertQualityInput{
		ScopeID:         "scope-g1",
		ChannelID:       "ch-1",
		Title:           "Media Studio 缺少小红书聚光资料",
		KnowledgeBaseID: "kb-a",
		Kind:            "missing",
		Note:            "工作台一键转单",
	})
	require.NoError(t, err)
	require.Equal(t, "open", iss.Status)
	require.Contains(t, iss.Description, "补充聚光能力页")
}

func TestRemediationBiDirectionalStrictLightweightMode(t *testing.T) {
	s := testService(t)
	ctx := consoleTestContext()
	operator, err := currentPrincipal(ctx)
	require.NoError(t, err)
	issue, err := s.CreateIssue(principalContext(testPrincipal()), gapInput())
	require.NoError(t, err)
	seedRepairChannel(t, s, issue)

	// Default: lightweight (revision=0) -> resolves directly
	view, err := s.remediation(ctx, issue)
	require.NoError(t, err)
	require.Equal(t, "lightweight", view.VerificationMode)

	// Save strict plan -> blocks resolve
	in := remediationInput{Cause: "answer", Acceptance: "clear answer", Paraphrase: "same meaning?", Counterexample: "boundary case?"}
	require.NoError(t, s.saveRemediation(ctx, operator, issue, in))
	_, err = s.UpdateIssue(ctx, issue.ID, IssueUpdate{Status: "resolved"})
	require.ErrorIs(t, err, errRemediationChanged)

	// Toggle strict OFF -> records audit & unlocks resolve!
	require.NoError(t, s.setRemediationMode(ctx, operator, issue, "lightweight", "确认口径无需三测"))
	view, err = s.remediation(ctx, issue)
	require.NoError(t, err)
	require.Equal(t, "lightweight", view.VerificationMode)
	_, err = s.UpdateIssue(ctx, issue.ID, IssueUpdate{Status: "resolved", Note: "轻量办结"})
	require.NoError(t, err)
}

func TestMultiTargetDeliveryIsolationAndSingleRetryGuard(t *testing.T) {
	s := testService(t)
	ctx := consoleTestContext()
	p, err := currentPrincipal(ctx)
	require.NoError(t, err)

	// Seed a multi-target pending run with 3 destinations
	now := time.Now().UTC()
	run := ReportRun{ID: "run-mt-1", TenantID: 1, ScheduleID: "adhoc", DueAt: now, Status: "pending", Snapshot: `{"total":1,"counts":{"open":1},"items":[]}`, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, s.db.Create(&run).Error)
	rows := []ReportRunDelivery{
		{ID: "d-ok", TenantID: 1, RunID: run.ID, SenderChannel: "ch-1", TargetKey: "group:g-mgmt", TargetKind: "group", TargetLabel: "管理层群", Status: "pending", IdempotencyKey: "k1", CreatedAt: now, UpdatedAt: now},
		{ID: "d-fail", TenantID: 1, RunID: run.ID, SenderChannel: "ch-1", TargetKey: "dm:uid-qbb", TargetKind: "dm", TargetLabel: "邱彬彬", Status: "pending", IdempotencyKey: "k2", CreatedAt: now, UpdatedAt: now},
		{ID: "d-unc", TenantID: 1, RunID: run.ID, SenderChannel: "ch-1", TargetKey: "dm:uid-zk", TargetKind: "dm", TargetLabel: "周凯", Status: "pending", IdempotencyKey: "k3", CreatedAt: now, UpdatedAt: now},
	}
	for _, r := range rows {
		require.NoError(t, s.db.Create(&r).Error)
	}

	calls := map[string]int{}
	s.MultiTargetDelivery = func(_ context.Context, _ uint64, _ string, target DeliveryTarget, _ string, _ *Report, _ *ReportArtifact) (string, error) {
		calls[target.Key]++
		if target.Key == "group:g-mgmt" {
			return "rcpt-ok-1", nil
		}
		if target.Key == "dm:uid-qbb" && calls[target.Key] == 1 {
			return "", ReportAccessError("rate_limited")
		}
		if target.Key == "dm:uid-qbb" {
			return "rcpt-retry-2", nil
		}
		return "", errors.New("network timeout after send")
	}

	res, err := s.deliverMultiTargetRun(ctx, p, run.ID, "")
	require.NoError(t, err)
	require.Equal(t, "uncertain", res.Status)
	require.Equal(t, 1, calls["group:g-mgmt"])
	require.Equal(t, 1, calls["dm:uid-qbb"])
	require.Equal(t, 1, calls["dm:uid-zk"])

	// Refuses to retry already-sent or uncertain targets!
	_, err = s.deliverMultiTargetRun(ctx, p, run.ID, "d-ok")
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.deliverMultiTargetRun(ctx, p, run.ID, "d-unc")
	require.ErrorIs(t, err, ErrConflict)

	// Retries ONLY failed target d-fail without touching d-ok or d-unc!
	res2, err := s.deliverMultiTargetRun(ctx, p, run.ID, "d-fail")
	require.NoError(t, err)
	require.Equal(t, "uncertain", res2.Status)
	require.Equal(t, 1, calls["group:g-mgmt"])
	require.Equal(t, 2, calls["dm:uid-qbb"])
	require.Equal(t, 1, calls["dm:uid-zk"])
}

func TestStrictModeToggleOnFreshIssueAndNicknameBackfill(t *testing.T) {
	s := testService(t)
	ctx := consoleTestContext()
	p, err := currentPrincipal(ctx)
	require.NoError(t, err)
	iss := &Issue{
		ID: "iss-fresh-1", TenantID: 1, ChannelID: "ch-1", ScopeID: "sc-1", ScopeName: "业务区",
		KnowledgeBaseID: "kb-1", Kind: "missing", Title: "缺少新版本说明", Status: "open",
		ReporterUID: "u-octo-42", ReporterName: "u-octo-42", Attachments: types.JSON("[]"), IdempotencyKey: "idem-fresh-1",
	}
	require.NoError(t, s.db.Create(iss).Error)

	// Toggle strict on fresh issue without prior remediation record
	require.NoError(t, s.setRemediationMode(ctx, p, iss, "strict", ""))
	view, err := s.remediation(ctx, iss)
	require.NoError(t, err)
	require.Equal(t, "strict", view.VerificationMode)
	require.Equal(t, 1, view.Revision)

	// Idempotent second strict call produces no duplicate event
	require.NoError(t, s.setRemediationMode(ctx, p, iss, "strict", ""))
	var evCount int64
	require.NoError(t, s.db.Model(&IssueEvent{}).Where("issue_id = ?", iss.ID).Count(&evCount).Error)
	require.Equal(t, int64(1), evCount)

	// Switch back to lightweight
	require.NoError(t, s.setRemediationMode(ctx, p, iss, "lightweight", "轻量闭环"))
	view, err = s.remediation(ctx, iss)
	require.NoError(t, err)
	require.Equal(t, "lightweight", view.VerificationMode)

	// Verify nickname backfill on GetIssue
	s.ResolveOctoUserNames = func(_ context.Context, _ uint64, _ string, uids []string) map[string]string {
		out := map[string]string{}
		for _, u := range uids {
			if u == "u-octo-42" {
				out[u] = "丘比特"
			}
		}
		return out
	}
	got, _, err := s.GetIssue(ctx, iss.ID)
	require.NoError(t, err)
	require.Equal(t, "丘比特", got.ReporterName)
}
