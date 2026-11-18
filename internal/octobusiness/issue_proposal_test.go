package octobusiness

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIssueConfirmationStoresOriginalFeedbackOnce(t *testing.T) {
	service := testService(t)
	reporter := testPrincipal()
	reporter.QuotedReplyID = "123"
	proposal, err := service.ProposeIssue(principalContext(reporter), gapInput())
	require.NoError(t, err)
	require.Equal(t, "pending", proposal.Status)
	require.Nil(t, proposal.Payload)
	var count int64
	require.NoError(t, service.db.Model(&Issue{}).Count(&count).Error)
	require.Zero(t, count)

	confirmer := reporter
	confirmer.MessageID = "confirmation-2"
	confirmer.MessageText = proposal.ConfirmationCommand
	otherUser := confirmer
	otherUser.UserID = "bob"
	_, err = service.Confirm(principalContext(otherUser), proposal.ID, false)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	otherScope := confirmer
	otherScope.ScopeID = "other-scope"
	_, err = service.Confirm(principalContext(otherScope), proposal.ID, false)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	revoked := confirmer
	revoked.KnowledgeBaseIDs = nil
	_, err = service.Confirm(principalContext(revoked), proposal.ID, false)
	require.Error(t, err)

	// The confirmation turn need not retrieve again. Evidence admission was
	// checked while preparing the original preview.
	confirmerCopy := confirmer
	confirmer.Validate = func(context.Context) (Principal, error) { return confirmerCopy, nil }
	ctx := WithPrincipal(context.Background(), confirmer)
	type outcome struct {
		proposal *Proposal
		err      error
	}
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			saved, confirmErr := service.Confirm(ctx, proposal.ID, false)
			results <- outcome{saved, confirmErr}
		})
	}
	workers.Wait()
	close(results)
	var issueID string
	for result := range results {
		require.NoError(t, result.err)
		require.Equal(t, "completed", result.proposal.Status)
		var saved map[string]string
		require.NoError(t, json.Unmarshal(result.proposal.Result, &saved))
		require.NotEmpty(t, saved["issue_id"])
		if issueID == "" {
			issueID = saved["issue_id"]
		}
		require.Equal(t, issueID, saved["issue_id"])
	}
	require.NoError(t, service.db.Model(&Issue{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	row, events, err := service.GetIssue(principalContext(reporter), issueID)
	require.NoError(t, err)
	require.Equal(t, reporter.MessageID, row.MessageID)
	require.Equal(t, reporter.MessageText, row.OriginalMessage)
	require.Len(t, events, 1)
	require.Contains(t, events[0].Note, reporter.QuotedReplyID)
}

func TestIssuePreviewRejectsUnverifiedGap(t *testing.T) {
	service := testService(t)
	ctx := principalContext(testPrincipal())
	ctx.Value(retrievalTraceKey{}).(*RetrievalTrace).failed = true
	_, err := service.ProposeIssue(ctx, gapInput())
	require.ErrorIs(t, err, ErrInvalid)
	var count int64
	require.NoError(t, service.db.Model(&Proposal{}).Count(&count).Error)
	require.Zero(t, count)
}
