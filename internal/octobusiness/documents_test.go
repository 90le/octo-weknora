package octobusiness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type managedDocumentService struct {
	interfaces.KnowledgeService
	list func(context.Context, string, *types.Pagination, types.KnowledgeListFilter) (*types.PageResult, error)
	get  func(context.Context, string) (*types.Knowledge, error)
}

func (s *managedDocumentService) ListPagedKnowledgeByKnowledgeBaseID(ctx context.Context, kb string, page *types.Pagination, filter types.KnowledgeListFilter) (*types.PageResult, error) {
	return s.list(ctx, kb, page, filter)
}
func (s *managedDocumentService) GetKnowledgeByID(ctx context.Context, id string) (*types.Knowledge, error) {
	return s.get(ctx, id)
}
func manualDocument(t *testing.T, id, kb, body, status string) *types.Knowledge {
	t.Helper()
	metadata, err := types.NewManualKnowledgeMetadata(body, status, 1).ToJSON()
	require.NoError(t, err)
	return &types.Knowledge{ID: id, TenantID: 1, KnowledgeBaseID: kb, Title: "Release draft", Type: types.KnowledgeTypeManual, Metadata: metadata, ParseStatus: "draft", EnableStatus: "disabled", FilePath: "/private/path-not-for-tools", UpdatedAt: time.Now()}
}

func TestManagedDocumentsIncludeUnboundDraftOnlyWithNativeManagementGrant(t *testing.T) {
	s := testService(t)
	p := testPrincipal()
	p.CanManageScope = true
	p.KnowledgeBaseIDs = nil
	row := manualDocument(t, "doc-a", "kb-a", "Private exact draft\nwith indentation", types.ManualKnowledgeStatusDraft)
	assertGrant := func(ctx context.Context) {
		require.True(t, access.HasKBGrant(ctx, "kb-a", 1, types.OrgRoleEditor))
		require.False(t, access.HasKBGrant(ctx, "kb-b", 1, types.OrgRoleEditor))
		require.Equal(t, "alice", types.CallerFromContext(ctx).UserID)
	}
	s.knowledge = &managedDocumentService{list: func(ctx context.Context, kb string, page *types.Pagination, filter types.KnowledgeListFilter) (*types.PageResult, error) {
		assertGrant(ctx)
		require.Equal(t, "kb-a", kb)
		require.Equal(t, "Release", filter.Keyword)
		return types.NewPageResult(1, page, []*types.Knowledge{row}), nil
	}, get: func(ctx context.Context, id string) (*types.Knowledge, error) {
		assertGrant(ctx)
		require.Equal(t, "doc-a", id)
		return row, nil
	}}
	ctx := WithRetrievalTrace(principalContext(p))
	result, err := s.Documents(ctx, ManagedDocumentQuery{Action: "list", Keyword: "Release", Page: 1, PageSize: 10})
	require.NoError(t, err)
	page := result.(*ManagedDocumentPage)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, "doc-a", page.Items[0].KnowledgeID)
	require.Equal(t, "draft", page.Items[0].PublicationStatus)
	require.Empty(t, page.Items[0].Content)
	result, err = s.Documents(ctx, ManagedDocumentQuery{Action: "get", KnowledgeID: "doc-a"})
	require.NoError(t, err)
	view := result.(ManagedDocument)
	require.True(t, view.ContentAvailable)
	require.Equal(t, "Private exact draft\nwith indentation", view.Content)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "/private/")
	require.NotContains(t, string(encoded), "metadata")
	trace := ctx.Value(retrievalTraceKey{}).(*RetrievalTrace)
	require.False(t, trace.succeeded, "management reads must not authorize a knowledge-gap registration")
}

func TestManagedDocumentsRejectQueryOnlyRevokedAndGuessedIDs(t *testing.T) {
	s := testService(t)
	calls := 0
	s.knowledge = &managedDocumentService{list: func(context.Context, string, *types.Pagination, types.KnowledgeListFilter) (*types.PageResult, error) {
		calls++
		return nil, nil
	}, get: func(context.Context, string) (*types.Knowledge, error) {
		calls++
		return manualDocument(t, "secret", "kb-b", "other KB draft", types.ManualKnowledgeStatusDraft), nil
	}}
	query := ManagedDocumentQuery{Action: "get", KnowledgeBaseID: "kb-a", KnowledgeID: "secret"}
	p := testPrincipal()
	p.CanManageScope = false
	_, err := s.Documents(principalContext(p), query)
	require.ErrorIs(t, err, ErrDenied)
	p.CanManageScope = true
	p.ManageKnowledgeBaseIDs = nil
	_, err = s.Documents(principalContext(p), query)
	require.ErrorIs(t, err, ErrDenied)
	p = testPrincipal()
	p.CanManageScope = true
	p.Validate = func(context.Context) (Principal, error) {
		fresh := p
		fresh.ManageKnowledgeBaseIDs = nil
		return fresh, nil
	}
	_, err = s.Documents(principalContext(p), query)
	require.ErrorIs(t, err, ErrDenied)
	require.Zero(t, calls)
	p = testPrincipal()
	p.CanManageScope = true
	_, err = s.Documents(principalContext(p), ManagedDocumentQuery{Action: "get", KnowledgeBaseID: "kb-b", KnowledgeID: "secret"})
	require.ErrorIs(t, err, ErrDenied)
	require.Zero(t, calls, "guessed KB is rejected before document access")
	_, err = s.Documents(principalContext(p), query)
	require.ErrorIs(t, err, ErrDenied)
	require.Equal(t, 1, calls, "persisted document-to-KB relationship must still match")
	p.TenantID = 2
	_, err = s.Documents(principalContext(p), query)
	require.Error(t, err)
	require.Equal(t, 1, calls, "cross-tenant KB never reaches native document service")
}

func TestManagedDocumentOutputNeverLeaksPartialBodyOrForeignListRows(t *testing.T) {
	row := manualDocument(t, "doc", "kb-a", strings.Repeat("x", managedDocumentContentLimit+1), types.ManualKnowledgeStatusDraft)
	view, err := managedDocumentView(row, true)
	require.NoError(t, err)
	require.False(t, view.ContentAvailable)
	require.Empty(t, view.Content)
	require.Contains(t, view.Notice, "native document editor")
	row.Type = "file"
	row.Metadata = types.JSON(`{"content":"internal metadata must not appear"}`)
	view, err = managedDocumentView(row, true)
	require.NoError(t, err)
	require.False(t, view.ContentAvailable)
	require.Empty(t, view.Content)
	s := testService(t)
	p := testPrincipal()
	p.CanManageScope = true
	s.knowledge = &managedDocumentService{list: func(ctx context.Context, kb string, page *types.Pagination, filter types.KnowledgeListFilter) (*types.PageResult, error) {
		return types.NewPageResult(1, page, []*types.Knowledge{manualDocument(t, "foreign", "kb-b", "foreign body", types.ManualKnowledgeStatusDraft)}), nil
	}}
	_, err = s.Documents(principalContext(p), ManagedDocumentQuery{Action: "list"})
	require.ErrorIs(t, err, ErrDenied)
}

func TestManagedDocumentToolRequiresSpecificParametersAndDoesNotGuessMultipleLibraries(t *testing.T) {
	s := testService(t)
	p := testPrincipal()
	p.CanManageScope = true
	tool := focusedToolByName(t, s, ToolKnowledgeDocuments)
	for _, payload := range []string{`{"action":"list","user_id":"other"}`, `{"action":"publish"}`, `{"action":"get"}`, `{"action":"list","page_size":51}`, `{"action":"list"}{}`} {
		result, err := tool.Execute(principalContext(p), json.RawMessage(payload))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Equal(t, "invalid_arguments", result.Data["error_code"])
	}
	p.ManageKnowledgeBaseIDs = []string{"kb-a", "kb-b"}
	result, err := tool.Execute(principalContext(p), json.RawMessage(`{"action":"list"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, "invalid_arguments", result.Data["error_code"])
}

func TestManagedDocumentsNewGrantWaitsForNextTurnAndRevocationStillDenies(t *testing.T) {
	s := testService(t)
	calls := 0
	s.knowledge = &managedDocumentService{get: func(ctx context.Context, id string) (*types.Knowledge, error) {
		calls++
		require.True(t, access.HasKBGrant(ctx, "kb-b", 1, types.OrgRoleEditor))
		return manualDocument(t, "doc-b", "kb-b", "newly granted draft", types.ManualKnowledgeStatusDraft), nil
	}}
	intake := testPrincipal()
	intake.CanManageScope = true
	fresh := intake
	fresh.ManageKnowledgeBaseIDs = []string{"kb-a", "kb-b"}
	intake.Validate = func(context.Context) (Principal, error) { return fresh, nil }
	query := ManagedDocumentQuery{Action: "get", KnowledgeBaseID: "kb-b", KnowledgeID: "doc-b"}
	_, err := s.Documents(principalContext(intake), query)
	require.ErrorIs(t, err, ErrDenied)
	require.Zero(t, calls, "a new grant must be covered by the next turn's persisted reply authority")
	next := fresh
	next.MessageID = "next-message"
	next.Validate = func(context.Context) (Principal, error) { return fresh, nil }
	result, err := s.Documents(principalContext(next), query)
	require.NoError(t, err)
	require.Equal(t, "newly granted draft", result.(ManagedDocument).Content)
	require.Equal(t, 1, calls)
	fresh.ManageKnowledgeBaseIDs = []string{"kb-a"}
	_, err = s.Documents(principalContext(next), query)
	require.ErrorIs(t, err, ErrDenied)
	require.Equal(t, 1, calls, "revocation must win even within an already admitted turn")
}
