package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// Exercises the real catalog, KB and message services over persisted data.
// A Web account without an Octo owner grant can read a KB-bound image only
// through evidence and authorization belonging to the KB.
func TestOctoArtifactIndependentKnowledgeAuthorization(t *testing.T) {
	catalog, db := newResourceCatalogForTest(t)
	require.NoError(t, db.AutoMigrate(
		&types.Session{}, &types.Message{}, &types.MessageArtifactRecord{}, &types.KnowledgeBase{}, &types.Knowledge{},
	))
	for _, sql := range []string{
		`CREATE TABLE im_channel_sessions (
			session_id text, tenant_id integer, im_channel_id text, platform text, deleted_at datetime)`,
		`CREATE TABLE im_channel_web_owners (
			tenant_id integer, im_channel_id text, web_user_id text, deleted_at datetime)`,
		`CREATE TABLE im_channels (id text, tenant_id integer, deleted_at datetime)`,
		`CREATE TABLE tenant_members (tenant_id integer, user_id text, status text, deleted_at datetime)`,
		`CREATE TABLE users (id text, is_active boolean, deleted_at datetime)`,
		`INSERT INTO im_channels VALUES ('octo-channel',7,NULL)`,
		`INSERT INTO users VALUES ('bob',1,NULL)`,
		`INSERT INTO tenant_members VALUES (7,'bob','active',NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	ctx := types.WithPrincipal(
		types.WithCaller(
			context.WithValue(context.Background(), types.UserIDContextKey, "bob"),
			types.Caller{TenantID: 7, UserID: "bob"},
		),
		types.Principal{Type: types.PrincipalWebUser, ID: "bob"},
	)
	ref, err := catalog.Register(ctx, 7, "local://7/exports/image.png", interfaces.ResourceRegistration{})
	require.NoError(t, err)
	imSession := &types.Session{TenantID: 7}
	webSession := &types.Session{TenantID: 7, UserID: "bob"}
	require.NoError(t, db.Create(imSession).Error)
	require.NoError(t, db.Create(webSession).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO im_channel_sessions VALUES (?,7,'octo-channel','octo',NULL)`, imSession.ID,
	).Error)
	repo := repository.NewMessageRepository(db)
	imMessage, err := repo.CreateMessage(ctx, &types.Message{
		SessionID: imSession.ID, Role: "assistant", Artifacts: types.MessageArtifacts{{URL: ref}},
	})
	require.NoError(t, err)
	// The message exists even if artifact resource binding failed.
	require.NotEmpty(t, imMessage.ID)
	require.NoError(t, db.Create(&types.KnowledgeBase{ID: "kb", TenantID: 7}).Error)
	require.NoError(t, db.Create(&types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", Type: "file"}).Error)
	require.NoError(t, catalog.Bind(
		ctx, ref, types.ResourceOwnerKnowledge, "doc", types.ResourceRelationExtractedImage,
	))
	webMessage, err := repo.CreateMessage(ctx, &types.Message{
		SessionID: webSession.ID, Role: "assistant", AgentTenantID: 7, Content: ref,
		KnowledgeReferences: types.References{&types.SearchResult{KnowledgeBaseID: "kb", Content: ref}},
	})
	require.NoError(t, err)
	noEvidence, err := repo.CreateMessage(ctx, &types.Message{
		SessionID: webSession.ID, Role: "assistant", AgentTenantID: 7, Content: ref,
	})
	require.NoError(t, err)
	matched, allowed, err := catalog.(access.OctoArtifactAccess).CheckOctoArtifactAccess(ctx, 7, ref)
	require.NoError(t, err)
	require.True(t, matched)
	require.False(t, allowed)
	kbs := &knowledgeBaseService{repo: repository.NewKnowledgeBaseRepository(db)}
	kb, err := repository.NewKnowledgeBaseRepository(db).GetKnowledgeBaseByID(ctx, "kb")
	require.NoError(t, err)
	grant, err := access.ResolveKB(
		ctx, access.KBRequest{Caller: types.CallerFromContext(ctx)}, kb, types.OrgRoleViewer, nil, nil,
	)
	require.NoError(t, err)
	for _, path := range []string{ref, "local://7/exports/image.png"} {
		_, err = access.ResolveKBFile(ctx, grant, "kb", path, catalog, catalog.(interfaces.KBResourceLookup))
		require.NoError(t, err)
	}
	messages := NewMessageService(repo, repository.NewSessionRepository(db), nil, nil, nil, nil, nil)
	shares := access.MessageKBShareAuthorizer{KBs: kbs}
	_, err = access.ResolveMessageFile(ctx, webSession.ID, webMessage.ID, ref, messages, nil, catalog, shares)
	require.NoError(t, err)
	_, err = access.ResolveMessageFile(ctx, webSession.ID, noEvidence.ID, ref, messages, nil, catalog, shares)
	require.ErrorIs(t, err, access.ErrForbidden)
	scoped := types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"other"}})
	_, err = access.ResolveMessageFile(scoped, webSession.ID, webMessage.ID, ref, messages, nil, catalog, shares)
	require.Error(t, err)
	_, err = catalog.Release(ctx, ref, types.ResourceOwnerKnowledge, "doc")
	require.NoError(t, err)
	_, err = access.ResolveMessageFile(ctx, webSession.ID, webMessage.ID, ref, messages, nil, catalog, shares)
	require.ErrorIs(t, err, access.ErrForbidden)
	_, err = access.ResolveKBFile(ctx, grant, "kb", ref, catalog, catalog.(interfaces.KBResourceLookup))
	require.ErrorIs(t, err, access.ErrForbidden)
}
