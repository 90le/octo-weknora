package octobusiness

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fixtureRepairQA struct {
	interfaces.SessionService
	t    *testing.T
	seen []string
}

func (qa *fixtureRepairQA) AgentQA(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
	require.True(qa.t, req.SharedAgentReadOnly)
	require.Equal(qa.t, []string{"kb-a"}, req.KnowledgeBaseIDs)
	qa.seen = append(qa.seen, req.Query)
	return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{
		FinalAnswer: "fixture result", ModelID: "isolated-model", KnowledgeRefs: []interface{}{
			&types.SearchResult{KnowledgeID: "published", KnowledgeBaseID: "kb-a", KnowledgeTitle: "rates.xlsx"},
		},
	}})
}

func seedRepairChannel(t *testing.T, s *Service, issue *Issue) {
	t.Helper()
	for _, name := range []string{"000037_octo_scopes", "000043_octo_knowledge_grants", "000056_octo_member_questions"} {
		sql, err := os.ReadFile("../../migrations/sqlite/" + name + ".up.sql")
		require.NoError(t, err)
		require.NoError(t, s.db.Exec(string(sql)).Error)
	}
	require.NoError(t, s.db.Exec("INSERT INTO octo_scopes (id,tenant_id,account_id,group_id,display_name,name_source,sync_status,verified_at,created_at,updated_at) VALUES (?,1,?,?,'fixture','octo','verified',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)", issue.ScopeID, issue.AccountID, issue.GroupID).Error)
	require.NoError(t, s.db.Exec("INSERT INTO octo_scope_bindings (tenant_id,scope_id,knowledge_base_id,created_at) VALUES (1,?,?,CURRENT_TIMESTAMP)", issue.ScopeID, issue.KnowledgeBaseID).Error)
	require.NoError(t, s.db.Exec(`CREATE TABLE custom_agents (id TEXT, tenant_id INTEGER, config TEXT, deleted_at TIMESTAMP);
		CREATE TABLE im_channels (id TEXT, tenant_id INTEGER, agent_id TEXT, enabled BOOLEAN, platform TEXT, credentials TEXT, deleted_at TIMESTAMP);
		INSERT INTO custom_agents VALUES ('agent',1,'{"agent_mode":"smart-reasoning"}',NULL)`).Error)
	require.NoError(t, s.db.Exec(`INSERT INTO im_channels VALUES (?,1,'agent',TRUE,'octo','{"account_id":"bot"}',NULL)`, issue.ChannelID).Error)
	issue.IdempotencyKey = qualityIssueKey(issue)
	require.NoError(t, s.db.Model(issue).Update("idempotency_key", issue.IdempotencyKey).Error)
	require.NoError(t, s.db.Exec(`INSERT INTO octo_message_inbox (tenant_id,channel_id,message_id,authority,input,reply,state,created_at,updated_at)
		VALUES (1,?,?,'{"AccountID":"bot","ScopeID":"scope-A","KnowledgeBaseIDs":["kb-a"]}','{"MessageType":"text","UserID":"alice","Content":"original question","Extra":{"octo_group_id":"group-A","octo_message_id":"10"}}','old answer','delivered',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, issue.ChannelID, issue.MessageID).Error)
}

func TestRemediationTracksPublishedSourceAndThreeVerifiedQuestions(t *testing.T) {
	s := testService(t)
	ctx := consoleTestContext()
	operator, err := currentPrincipal(ctx)
	require.NoError(t, err)
	issue, err := s.CreateIssue(principalContext(testPrincipal()), gapInput())
	require.NoError(t, err)
	seedRepairChannel(t, s, issue)
	require.NoError(t, s.db.Exec("CREATE TABLE knowledges (id TEXT PRIMARY KEY, tenant_id INTEGER, knowledge_base_id TEXT, channel TEXT, enable_status TEXT, parse_status TEXT, processed_at DATETIME, metadata TEXT, deleted_at DATETIME)").Error)
	putDoc := func(id, kb, source, external, version, candidate string) {
		meta, err := json.Marshal(map[string]string{"datasource_id": source, "external_id": external, "source_version": version, "source_path": "pricing/rates.xlsx", "sync_target_external_id": candidate})
		require.NoError(t, err)
		require.NoError(t, s.db.Exec("INSERT INTO knowledges VALUES (?,1,?,'local_folder','enabled','completed',?,?,NULL)", id, kb, time.Now().UTC(), string(meta)).Error)
	}
	putDoc("old", "kb-a", "folder", "file", "v1", "")
	putDoc("new", "kb-a", "folder", "staging", "v2", "file")
	putDoc("other", "kb-b", "folder", "file", "v3", "")
	putDoc("same-name", "kb-a", "other-folder", "file", "v3", "")
	in := remediationInput{Cause: "material", Acceptance: "Use the current unit and cite the rate", Paraphrase: "What is the unit?", Counterexample: "Does this apply outside the pilot?", WaitForUpdate: true, DocumentID: "other"}
	require.ErrorIs(t, s.saveRemediation(ctx, operator, issue, in), gorm.ErrRecordNotFound)
	in.DocumentID = "new"
	require.ErrorIs(t, s.saveRemediation(ctx, operator, issue, in), ErrInvalid)
	in.DocumentID = "old"
	require.NoError(t, s.saveRemediation(ctx, operator, issue, in))
	require.ErrorIs(t, s.saveRemediation(ctx, operator, issue, in), ErrConflict)
	view, err := s.remediation(ctx, issue)
	require.NoError(t, err)
	require.Equal(t, "waiting", view.State)
	queue, err := s.ListIssues(ctx, IssueFilter{Visibility: "mine"})
	require.NoError(t, err)
	require.EqualValues(t, 1, queue.Total)
	_, err = s.prepareRun(ctx, issue, "original question", "original", 1)
	require.ErrorIs(t, err, errRemediationChanged)
	require.NoError(t, s.db.Exec("UPDATE knowledges SET deleted_at=? WHERE id='old'", time.Now().UTC()).Error)
	view, err = s.remediation(ctx, issue)
	require.NoError(t, err)
	require.Equal(t, "unavailable", view.State)
	putDoc("published", "kb-a", "folder", "file", "v2", "")
	run, err := s.prepareRun(ctx, issue, "original question", "paraphrase", 1)
	require.NoError(t, err)
	require.Equal(t, in.Paraphrase, run.Question)
	require.Equal(t, "published", run.DocumentID)
	_, err = s.UpdateIssue(ctx, issue.ID, IssueUpdate{Status: "resolved"})
	require.ErrorIs(t, err, errRemediationChanged)
	qa := &fixtureRepairQA{t: t}
	for _, scenario := range []string{"original", "paraphrase", "counterexample"} {
		reply, err := s.retestIssue(ctx, qa, issue.ID, scenario, 1)
		require.NoError(t, err)
		_, err = s.reviewIssueRetest(ctx, operator, issue, reply.ID, retestReview{State: "verified", Note: "checked fixture"})
		require.NoError(t, err)
	}
	require.Equal(t, []string{"original question", in.Paraphrase, in.Counterexample}, qa.seen)
	require.NoError(t, s.requireVerifiedRepair(ctx, issue))
	_, err = s.UpdateIssue(ctx, issue.ID, IssueUpdate{Status: "resolved"})
	require.NoError(t, err)
	require.NoError(t, s.db.Exec("UPDATE knowledges SET enable_status='disabled' WHERE id='published'").Error)
	require.ErrorIs(t, s.validateRunVersion(ctx, issue, run), errRemediationChanged)
	require.ErrorIs(t, s.requireVerifiedRepair(ctx, issue), errRemediationChanged)
}

func TestRemediationRoutesNonMaterialAndMissingSources(t *testing.T) {
	s := testService(t)
	ctx := consoleTestContext()
	operator, err := currentPrincipal(ctx)
	require.NoError(t, err)
	issue, err := s.CreateIssue(principalContext(testPrincipal()), gapInput())
	require.NoError(t, err)
	for i, cause := range []string{"material", "retrieval", "permission", "service", "answer"} {
		in := remediationInput{Revision: i, Cause: cause, Acceptance: "expected", Paraphrase: "paraphrase", Counterexample: "counterexample"}
		require.NoError(t, s.saveRemediation(ctx, operator, issue, in))
		view, err := s.remediation(ctx, issue)
		require.NoError(t, err)
		if cause == "material" {
			require.Equal(t, "needs_source", view.State)
		} else {
			require.Equal(t, "ready", view.State)
		}
	}
}
