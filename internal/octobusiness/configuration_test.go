package octobusiness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigurationListsUnboundManagedTargetsWithoutWideningRetrieval(t *testing.T) {
	service := testService(t)
	p := testPrincipal()
	p.ManageKnowledgeBaseIDs = []string{"kb-a", "kb-b", "kb-other"}
	ctx := principalContext(p)
	configuration, err := service.Configuration(ctx)
	require.NoError(t, err)
	require.Equal(t, []KnowledgeTarget{{KnowledgeBaseID: "kb-a", Name: "Product A"}}, configuration.ReadableKnowledgeBases)
	require.Equal(t, []KnowledgeTarget{{KnowledgeBaseID: "kb-a", Name: "Product A"}, {KnowledgeBaseID: "kb-b", Name: "Product B"}}, configuration.ManageableKnowledgeBases)
	require.Equal(t, p.ScopeID, configuration.ScopeID)
	after, ok := PrincipalFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, []string{"kb-a"}, after.KnowledgeBaseIDs)
	_, err = service.checkKB(ctx, p, "kb-b", false)
	require.ErrorIs(t, err, ErrDenied)
	p.ManageKnowledgeBaseIDs = nil
	revoked := p
	p.Validate = func(context.Context) (Principal, error) { return revoked, nil }
	configuration, err = service.Configuration(WithPrincipal(context.Background(), p))
	require.NoError(t, err)
	require.Empty(t, configuration.ManageableKnowledgeBases)
}
func TestConfigurationWithNoGrantedIDsDoesNotListWorkspaceAssets(t *testing.T) {
	service := testService(t)
	p := testPrincipal()
	p.KnowledgeBaseIDs = nil
	p.ManageKnowledgeBaseIDs = nil
	configuration, err := service.Configuration(principalContext(p))
	require.NoError(t, err)
	require.Empty(t, configuration.ReadableKnowledgeBases)
	require.Empty(t, configuration.ManageableKnowledgeBases)
}
