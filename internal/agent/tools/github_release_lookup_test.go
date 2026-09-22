package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	githubconnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type releaseToolKB struct {
	interfaces.KnowledgeBaseService
}

func (*releaseToolKB) GetKnowledgeBaseByIDOnly(_ context.Context, id string) (*types.KnowledgeBase, error) {
	return &types.KnowledgeBase{ID: id, TenantID: 7}, nil
}

type releaseToolDataSources struct {
	interfaces.DataSourceRepository
	rows map[string][]*types.DataSource
}

func (r *releaseToolDataSources) FindByKnowledgeBase(_ context.Context, kbID string) ([]*types.DataSource, error) {
	return append([]*types.DataSource(nil), r.rows[kbID]...), nil
}

func releaseToolContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	return context.WithValue(ctx, types.UserIDContextKey, "release-fixture")
}

func releaseToolSource(t *testing.T, id, kb, mode string) *types.DataSource {
	t.Helper()
	config := &types.DataSourceConfig{
		Type:        types.ConnectorTypeGitHub,
		Credentials: map[string]interface{}{"access_token": "tool-secret"},
		Settings:    map[string]interface{}{"repository": "Acme/Widget", "mode": mode},
	}
	blob, err := config.ToJSON()
	require.NoError(t, err)
	return &types.DataSource{ID: id, TenantID: 7, KnowledgeBaseID: kb, Type: types.ConnectorTypeGitHub, Config: blob, Status: types.DataSourceStatusActive}
}

func releaseToolCatalog(t *testing.T, tool *GitHubReleaseLookupTool) githubReleaseCatalog {
	t.Helper()
	result, err := tool.Execute(releaseToolContext(), json.RawMessage(`{"action":"list"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	var catalog githubReleaseCatalog
	require.NoError(t, json.Unmarshal([]byte(result.Output), &catalog))
	return catalog
}

func TestGitHubReleaseLookupUsesOpaqueScopeBoundReferences(t *testing.T) {
	tool := NewGitHubReleaseLookupTool(
		&releaseToolDataSources{rows: map[string][]*types.DataSource{
			"kb-uuid": {
				releaseToolSource(t, "document-source", "kb-uuid", "documents"),
				releaseToolSource(t, "source-source", "kb-uuid", "source"),
			},
		}},
		&releaseToolKB{},
		types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-uuid", TenantID: 7}},
		githubconnector.NewReleaseCatalogCache(), nil,
	)
	catalog := releaseToolCatalog(t, tool)
	require.Equal(t, githubReleaseCatalog{Repositories: []githubReleaseCatalogEntry{{ReleaseRef: "r1", Repository: "Acme/Widget"}}, Complete: true}, catalog)

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(tool.Parameters(), &schema))
	require.Contains(t, schema.Properties, "release_ref")
	require.NotContains(t, schema.Properties, "knowledge_base_id")
	require.NotContains(t, schema.Properties, "data_source_id")
	require.NotContains(t, schema.Properties, "repository")
}

func TestGitHubReleaseLookupLatestReturnsTrustedReleaseCitation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer tool-secret", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/Acme/Widget/releases/latest":
			_, _ = w.Write([]byte(`{"id":9,"tag_name":"v1.2.3","name":"Release","body":"- fixed","published_at":"2026-09-21T10:41:58Z","created_at":"2026-09-21T10:41:58Z"}`))
		case "/repos/Acme/Widget/releases":
			_, _ = w.Write([]byte(`[{"id":9,"tag_name":"v1.2.3","name":"Release","body":"- fixed","published_at":"2026-09-21T10:41:58Z","created_at":"2026-09-21T10:41:58Z"}]`))
		default:
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	tool := NewGitHubReleaseLookupTool(
		&releaseToolDataSources{rows: map[string][]*types.DataSource{"kb-secret": {releaseToolSource(t, "ds-secret", "kb-secret", "source")}}},
		&releaseToolKB{},
		types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-secret", TenantID: 7}},
		githubconnector.NewReleaseCatalogCache(), githubconnector.NewConnectorWithHTTPClient(server.Client(), server.URL),
	)
	catalog := releaseToolCatalog(t, tool)
	require.Len(t, catalog.Repositories, 1)
	result, err := tool.Execute(releaseToolContext(), json.RawMessage(`{"action":"latest","release_ref":"r1"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Output, "https://github.com/Acme/Widget/releases/tag/v1.2.3")
	require.NotContains(t, result.Output, "ds-secret")
	require.NotContains(t, result.Output, "kb-secret")
	require.NotContains(t, result.Output, "tool-secret")
	citation, ok := result.Data[types.GitHubReleaseCitationDataKey].(types.GitHubReleaseCitation)
	require.True(t, ok)
	require.Equal(t, "kb-secret", citation.KnowledgeBaseID)
	require.Equal(t, "ds-secret", citation.DataSourceID)
	require.Equal(t, "v1.2.3", citation.TagName)
	require.False(t, citation.PublishedAt.IsZero())

	// A release history is useful context, but never proves that the first
	// returned page is the current GitHub latest release.
	history, err := tool.Execute(releaseToolContext(), json.RawMessage(`{"action":"history","release_ref":"r1"}`))
	require.NoError(t, err)
	require.True(t, history.Success, history.Error)
	_, hasHistoryCitation := history.Data[types.GitHubReleaseCitationDataKey]
	require.False(t, hasHistoryCitation)

	// A pinned file/tag target must not become a whole-KB release grant.
	pinned := NewGitHubReleaseLookupTool(
		&releaseToolDataSources{rows: map[string][]*types.DataSource{"kb-secret": {releaseToolSource(t, "ds-secret", "kb-secret", "source")}}},
		&releaseToolKB{},
		types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb-secret", TenantID: 7, KnowledgeIDs: []string{"doc"}}},
		githubconnector.NewReleaseCatalogCache(), githubconnector.NewConnectorWithHTTPClient(server.Client(), server.URL),
	)
	blocked, err := pinned.Execute(releaseToolContext(), json.RawMessage(`{"action":"list"}`))
	require.NoError(t, err)
	require.True(t, blocked.Success)
	var empty githubReleaseCatalog
	require.NoError(t, json.Unmarshal([]byte(blocked.Output), &empty))
	require.Empty(t, empty.Repositories)

}

func TestGitHubReleaseLookupNoStableFallbackDoesNotCreateLatestCitation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Acme/Widget/releases/latest":
			w.WriteHeader(http.StatusNotFound)
		case "/repos/Acme/Widget/releases":
			// The history endpoint is intentionally incomplete; even a successful
			// fallback must not establish a latest-version evidence marker.
			w.Header().Set("Link", `<https://api.github.com/repos/Acme/Widget/releases?page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"id":8,"tag_name":"v2.0.0-rc.1","published_at":"2026-09-21T10:41:58Z","created_at":"2026-09-21T10:41:58Z","prerelease":true}]`))
		case "/repos/Acme/Widget/tags":
			_, _ = w.Write([]byte(`[{"name":"v2.0.0-rc.1","commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]`))
		default:
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	tool := NewGitHubReleaseLookupTool(
		&releaseToolDataSources{rows: map[string][]*types.DataSource{"kb-secret": {releaseToolSource(t, "ds-secret", "kb-secret", "source")}}},
		&releaseToolKB{},
		types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-secret", TenantID: 7}},
		githubconnector.NewReleaseCatalogCache(), githubconnector.NewConnectorWithHTTPClient(server.Client(), server.URL),
	)
	catalog := releaseToolCatalog(t, tool)
	require.Len(t, catalog.Repositories, 1)
	result, err := tool.Execute(releaseToolContext(), json.RawMessage(`{"action":"latest","release_ref":"r1"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Output, `"no_published_stable_release":true`)
	require.Contains(t, result.Output, `"history_complete":false`)
	_, hasCitation := result.Data[types.GitHubReleaseCitationDataKey]
	require.False(t, hasCitation)
}
