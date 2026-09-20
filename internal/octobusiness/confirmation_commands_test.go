package octobusiness

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProposalPublishesExactConfirmationCommandsAndExplainsIncompleteText(t *testing.T) {
	service := testService(t)
	backend := &fakeKB{}
	service.kb = backend
	p := testPrincipal()
	proposal, err := service.Propose(principalContext(p), ManagementInput{Action: "rename", KnowledgeBaseID: "kb-a", Name: "确认指令验收"})
	require.NoError(t, err)
	require.Equal(t, "确认 "+proposal.ID, proposal.ConfirmationCommand)
	require.Equal(t, "取消 "+proposal.ID, proposal.CancellationCommand)
	p.MessageID = "natural-confirm"
	p.MessageText = "确认创建"
	_, err = service.Confirm(principalContext(p), proposal.ID, false)
	require.ErrorIs(t, err, ErrInvalid)
	require.Contains(t, err.Error(), proposal.ConfirmationCommand)
	require.Zero(t, backend.calls)
	var stored Proposal
	require.NoError(t, service.db.First(&stored, "id = ?", proposal.ID).Error)
	require.Equal(t, "pending", stored.Status)
	stranger := p
	stranger.UserID = "another-user"
	_, err = service.Confirm(principalContext(stranger), proposal.ID, false)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "another user never receives actionable details of the pending proposal")
	p.MessageID = "message-1"
	p.MessageText = proposal.ConfirmationCommand
	_, err = service.Confirm(principalContext(p), proposal.ID, false)
	require.ErrorIs(t, err, ErrDenied, "the proposal's original message cannot also confirm it")
	p.MessageID = "exact-confirm"
	p.MessageText = proposal.ConfirmationCommand
	done, err := service.Confirm(principalContext(p), proposal.ID, false)
	require.NoError(t, err)
	require.Equal(t, "completed", done.Status)
	require.Equal(t, 1, backend.calls)
}
