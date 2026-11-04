package session

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type channelReferenceMessages struct {
	interfaces.MessageService
	knowledge map[string]*types.Knowledge
}

func (s *channelReferenceMessages) ChannelChatKnowledge(_ context.Context, id string) (*types.Knowledge, error) {
	if k := s.knowledge[id]; k != nil {
		return k, nil
	}
	return nil, errors.New("missing")
}

type channelReferenceKBs struct {
	interfaces.KnowledgeBaseService
	values map[string]*types.KnowledgeBase
}

func (s *channelReferenceKBs) GetKnowledgeBaseByIDOnly(_ context.Context, id string) (*types.KnowledgeBase, error) {
	if kb := s.values[id]; kb != nil {
		return kb, nil
	}
	return nil, errors.New("missing")
}

type channelReferenceShares struct {
	interfaces.KBShareService
	allowed bool
	fail    bool
}

func (s *channelReferenceShares) CheckTenantKBPermission(
	context.Context, string, uint64, types.TenantRole,
) (types.OrgMemberRole, bool, error) {
	if s.fail {
		return "", false, errors.New("permission lookup failed")
	}
	return types.OrgRoleViewer, s.allowed, nil
}

func TestChannelChatReferenceAuthorization(t *testing.T) {
	lookup := &channelReferenceMessages{knowledge: map[string]*types.Knowledge{
		"own":    {ID: "own", TenantID: 7, KnowledgeBaseID: "own-kb"},
		"shared": {ID: "shared", TenantID: 8, KnowledgeBaseID: "shared-kb"},
	}}
	kbs := &channelReferenceKBs{values: map[string]*types.KnowledgeBase{
		"own-kb": {ID: "own-kb", TenantID: 7}, "shared-kb": {ID: "shared-kb", TenantID: 8},
	}}
	shares := &channelReferenceShares{allowed: true}
	h := &Handler{messageService: lookup, knowledgebaseService: kbs, kbShareService: shares}
	ctx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "alice", Role: types.TenantRoleViewer,
	})
	input := func() []*types.Message {
		return []*types.Message{{Role: "assistant", KnowledgeReferences: types.References{
			{
				ID: "1", KnowledgeID: "own", Content: "snippet",
				Metadata: map[string]string{"token": "secret"}, ImageInfo: "secret",
			},
			{ID: "2", KnowledgeID: "shared", KnowledgeBaseID: "shared-kb", Content: "shared"},
			{ID: "3", KnowledgeID: "own", KnowledgeBaseID: "wrong", Content: "mismatch"},
			{ID: "4", KnowledgeID: "missing", Content: "missing"},
			{ID: "5", Content: "unknown"},
		}}}
	}
	messages := input()
	h.filterChannelChatReferences(ctx, messages)
	require.Len(t, messages[0].KnowledgeReferences, 2)
	require.Equal(t, "own-kb", messages[0].KnowledgeReferences[0].KnowledgeBaseID)
	require.Equal(t, "snippet", messages[0].KnowledgeReferences[0].Content)
	require.Empty(t, messages[0].KnowledgeReferences[0].Metadata)
	require.Empty(t, messages[0].KnowledgeReferences[0].ImageInfo)
	shares.allowed = false
	messages = input()
	h.filterChannelChatReferences(ctx, messages)
	require.Len(t, messages[0].KnowledgeReferences, 1)
	shares.allowed = true
	shares.fail = true
	messages = input()
	h.filterChannelChatReferences(ctx, messages)
	require.Len(t, messages[0].KnowledgeReferences, 1)
	require.Equal(t, "own", messages[0].KnowledgeReferences[0].KnowledgeID)
	delete(kbs.values, "own-kb")
	messages = input()
	h.filterChannelChatReferences(ctx, messages)
	require.Empty(t, messages[0].KnowledgeReferences)
}

func TestChannelChatArtifactHandleHiddenWithThoughts(t *testing.T) {
	const visible = "resource://AbCdEfGhIjKlMnOpQrStUv"
	const hidden = "resource://abcdefghijklmnopqrstuv"
	msg := &types.Message{
		Role: "assistant", Content: "<think>" + hidden + "</think>![image](" + visible + ")",
		Artifacts: types.MessageArtifacts{{URL: visible}, {URL: hidden}},
	}
	stripChannelChatThoughts(msg)
	require.Equal(t, visible, msg.Artifacts[0].URL)
	require.Empty(t, msg.Artifacts[1].URL)
}
