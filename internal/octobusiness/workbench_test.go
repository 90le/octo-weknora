package octobusiness

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssueEvidenceUsesTheAdmittedQuestionInTheSameScope(t *testing.T) {
	s := testService(t)
	p := testPrincipal()
	p.MessageID = "octo:bot:30"
	row, err := s.CreateIssue(principalContext(p), gapInput())
	require.NoError(t, err)
	require.NoError(t, s.db.Exec(`CREATE TABLE octo_message_inbox (
		tenant_id BIGINT, channel_id TEXT, message_id TEXT, authority TEXT, input TEXT, reply TEXT, state TEXT
	)`).Error)
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
	insert := "INSERT INTO octo_message_inbox VALUES (1,'group-A',?,?,?,?, 'delivered')"
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
