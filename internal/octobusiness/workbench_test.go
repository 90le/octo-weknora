package octobusiness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetestKeepsOneActiveAttemptAndSeparatesReviewFromIssueStatus(t *testing.T) {
	s := testService(t)
	ctx := consoleTestContext()
	p, err := currentPrincipal(ctx)
	require.NoError(t, err)
	issue, err := s.CreateIssue(principalContext(testPrincipal()), gapInput())
	require.NoError(t, err)
	now := time.Now().UTC()
	old, err := s.beginIssueRetest(ctx, p, issue, "123", now.Add(-3*time.Minute))
	require.NoError(t, err)
	run, err := s.beginIssueRetest(ctx, p, issue, "123", now)
	require.NoError(t, err)
	_, err = s.beginIssueRetest(ctx, p, issue, "123", now)
	require.ErrorIs(t, err, ErrConflict)
	result := &issueRetest{Answer: "New answer", ModelID: "qa-model", Sources: []string{"Guide"}}
	require.ErrorIs(t, s.finishIssueRetest(ctx, issue, old, result, nil), ErrConflict)
	require.NoError(t, s.finishIssueRetest(ctx, issue, run, result, nil))
	input := retestReview{State: "verified", Note: "Compared with the guidance"}
	reviewed, err := s.reviewIssueRetest(ctx, p, issue, run.ID, input)
	require.NoError(t, err)
	require.Equal(t, result.Sources, reviewed.Sources)
	require.Equal(t, result.Answer, reviewed.Answer)
	_, err = s.reviewIssueRetest(ctx, p, issue, run.ID, input)
	require.NoError(t, err)
	_, err = s.reviewIssueRetest(ctx, p, issue, run.ID, retestReview{State: "unresolved", Note: "New conclusion"})
	require.ErrorIs(t, err, ErrConflict)
	current, _, err := s.GetIssue(ctx, issue.ID)
	require.NoError(t, err)
	require.Equal(t, "open", current.Status)
	metrics := map[string]int64{}
	ids := s.db.Model(&Issue{}).Select("id").Where("id = ?", issue.ID)
	require.NoError(t, s.addReportRetests(context.Background(), ids, p.TenantID,
		now.Add(-time.Minute), now.Add(time.Minute), metrics))
	require.EqualValues(t, 1, metrics["retest_completed"])
	require.EqualValues(t, 1, metrics["retest_verified"])
	require.EqualValues(t, 1, metrics["retest_failed"])
}

func TestIssueEvidenceUsesTheAdmittedQuestionInTheSameScope(t *testing.T) {
	s := testService(t)
	p := testPrincipal()
	p.MessageID = "octo:bot:30"
	row, err := s.CreateIssue(principalContext(p), gapInput())
	require.NoError(t, err)
	auth := `{"AccountID":"bot","ScopeID":"scope-A","KnowledgeBaseIDs":["kb-a"]}`
	payload := `{"reply":{"message_id":"20","payload":{"reply":{"message_id":10,"from_uid":"alice"}}}}`
	extra := map[string]string{"octo_group_id": "group-A", "octo_message_id": "30", "octo_original_payload": payload}
	feedback, err := json.Marshal(map[string]any{
		"UserID": "alice", "Extra": extra, "Quote": map[string]any{
			"SenderID": "bot", "MessageID": "20", "Content": "旧答复", "IsBotMessage": true,
		},
	})
	require.NoError(t, err)
	question := `{"MessageType":"text","UserID":"alice","Content":"@Octo 小测 原问题",` +
		`"Extra":{"octo_group_id":"group-A","octo_message_id":"10","octo_addressed":"true","octo_question_text":"原问题"}}`
	insert := `INSERT INTO octo_message_inbox
		(tenant_id,channel_id,message_id,authority,input,reply,state,created_at,updated_at)
		VALUES (1,'group-A',?,?,?,?,'delivered',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`
	require.NoError(t, s.db.Exec(insert, p.MessageID, auth, string(feedback), "").Error)
	require.NoError(t, s.db.Exec(insert, "octo:bot:10", auth, question, "旧答复").Error)
	admin := principalContext(Principal{TenantID: 1, ChannelID: "console", UserID: "admin", Console: true})
	evidence, err := s.issueEvidence(admin, row)
	require.NoError(t, err)
	require.Equal(t, &issueEvidence{
		Question: "原问题", QuestionMessageID: "10", Answer: "旧答复", AnswerMessageID: "20",
	}, evidence)
	require.NoError(t, s.db.Exec("UPDATE octo_message_inbox SET authority = ? WHERE message_id = 'octo:bot:10'",
		`{"AccountID":"bot","ScopeID":"other-scope","KnowledgeBaseIDs":["kb-a"]}`).Error)
	evidence, err = s.issueEvidence(admin, row)
	require.NoError(t, err)
	require.Empty(t, evidence.Question)
	_, err = s.issueEvidence(principalContext(p), row)
	require.ErrorIs(t, err, ErrDenied)
}

func TestIssueAssignmentUsesOnlyContactsFromItsKnowledgeBase(t *testing.T) {
	s := testService(t)
	admin := principalContext(Principal{TenantID: 1, ChannelID: "console", UserID: "admin", Console: true})
	row, err := s.CreateIssue(principalContext(testPrincipal()), gapInput())
	require.NoError(t, err)
	other, err := s.PutContact(admin, Contact{KnowledgeBaseID: "kb-b", Name: "其他库负责人", UID: "other"})
	require.NoError(t, err)
	_, err = s.UpdateIssue(admin, row.ID, IssueUpdate{Status: "inprogress", OwnerContactID: other.ID})
	require.Error(t, err)
	contact, err := s.PutContact(admin, Contact{KnowledgeBaseID: "kb-a", Name: "资料负责人", UID: "owner"})
	require.NoError(t, err)
	assigned, err := s.UpdateIssue(admin, row.ID, IssueUpdate{Status: "inprogress", OwnerContactID: contact.ID})
	require.NoError(t, err)
	require.Equal(t, "owner", assigned.OwnerUID)
	require.Equal(t, "资料负责人", assigned.OwnerName)
}
