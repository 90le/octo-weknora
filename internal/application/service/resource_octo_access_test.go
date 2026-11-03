package service

import (
	"context"
	"io"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	filesvc "github.com/Tencent/WeKnora/internal/application/service/file"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These tables are present in a migrated deployment; shared-file tests use a
// deliberately minimal resource-only database and need the origin schema too.
func addOctoOriginTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, sql := range []string{
		`CREATE TABLE sessions (id text, tenant_id integer)`,
		`CREATE TABLE messages (id text, session_id text)`,
		`CREATE TABLE message_artifacts (message_id text, session_id text, url text, deleted_at datetime)`,
		`CREATE TABLE im_channel_sessions (session_id text, tenant_id integer, im_channel_id text,
			platform text, deleted_at datetime)`,
		`CREATE TABLE im_channel_web_owners (tenant_id integer, im_channel_id text,
			web_user_id text, deleted_at datetime)`,
		`CREATE TABLE im_channels (id text, tenant_id integer, deleted_at datetime)`,
		`CREATE TABLE tenant_members (tenant_id integer, user_id text, status text, deleted_at datetime)`,
		`CREATE TABLE users (id text, is_active boolean, deleted_at datetime)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
}

func TestUnboundOctoArtifactFileAccess(t *testing.T) {
	catalog, db := newResourceCatalogForTest(t)
	addOctoOriginTables(t, db)
	for _, sql := range []string{
		`INSERT INTO sessions VALUES ('session',7)`,
		`INSERT INTO messages VALUES ('message','session')`,
		`INSERT INTO im_channel_sessions VALUES ('session',7,'channel','octo',NULL)`,
		`INSERT INTO im_channels VALUES ('channel',7,NULL)`,
		`INSERT INTO users VALUES ('alice',1,NULL),('bob',1,NULL)`,
		`INSERT INTO tenant_members VALUES (7,'alice','active',NULL),(7,'bob','active',NULL)`,
		`INSERT INTO im_channel_web_owners VALUES (7,'channel','alice',NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	ctx := func(user string) context.Context {
		return types.WithPrincipal(
			types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: user}),
			types.Principal{Type: types.PrincipalWebUser, ID: user},
		)
	}
	registered, err := catalog.Register(
		context.Background(),
		7,
		"local://7/exports/registered.pdf",
		interfaces.ResourceRegistration{},
	)
	require.NoError(t, err)
	cloud, err := catalog.Register(
		context.Background(),
		7,
		"storage://first/s3://bucket/7/other.pdf",
		interfaces.ResourceRegistration{},
	)
	require.NoError(t, err)
	cloudAlias, err := catalog.Register(
		context.Background(),
		7,
		"storage://second/s3://bucket/7/other.pdf",
		interfaces.ResourceRegistration{},
	)
	require.NoError(t, err)
	for _, artifact := range []struct {
		stored   string
		accesses []string
	}{
		{registered, []string{registered, "local://7/exports/registered.pdf"}},
		{"local://7/exports/./raw.pdf", []string{
			"local://7/exports/raw.pdf", "storage://backend/local://7/exports/raw.pdf",
		}},
		{"s3://bucket/7/report.pdf", []string{
			"s3://bucket/7/report.pdf", "storage://backend/s3://bucket/7/report.pdf",
		}},
		{cloud, []string{cloud, cloudAlias, "s3://bucket/7/other.pdf"}},
	} {
		require.NoError(
			t,
			db.Exec(
				`INSERT INTO message_artifacts VALUES (?,?,?,NULL)`,
				"message",
				"session",
				artifact.stored,
			).Error,
		)
		for _, ref := range artifact.accesses {
			matched, err := access.CheckOctoFile(ctx("bob"), catalog, 7, ref)
			require.True(t, matched, ref)
			require.ErrorIs(t, err, access.ErrForbidden, ref)
			matched, err = access.CheckOctoFile(ctx("alice"), catalog, 7, ref)
			require.True(t, matched, ref)
			require.NoError(t, err, ref)
		}
		msg := &types.Message{
			ID:            "message",
			SessionID:     "session",
			AgentTenantID: 7,
			Content:       artifact.stored,
			Artifacts:     types.MessageArtifacts{{URL: artifact.stored}},
		}
		_, err := access.AuthorizeMessageFile(
			ctx("bob"),
			msg,
			artifact.stored,
			nil,
			catalog,
			access.MessageKBShareAuthorizer{},
		)
		require.ErrorIs(t, err, access.ErrForbidden)
		_, err = access.ResolveMessageArtifact(ctx("bob"), msg, 0, nil, catalog, access.MessageKBShareAuthorizer{})
		require.ErrorIs(t, err, access.ErrForbidden)
	}
	matched, err := access.CheckOctoFile(ctx("bob"), catalog, 7, "local://7/exports/no-origin.pdf")
	require.True(t, matched)
	require.ErrorIs(t, err, access.ErrForbidden)
}

func TestTraceableOctoArtifactAccess(t *testing.T) {
	catalog, db := newResourceCatalogForTest(t)
	addOctoOriginTables(t, db)
	for _, sql := range []string{
		`INSERT INTO sessions VALUES ('session',7)`,
		`INSERT INTO messages VALUES ('message','session')`,
		`INSERT INTO im_channel_sessions VALUES ('session',7,'channel','octo',CURRENT_TIMESTAMP)`,
		`INSERT INTO im_channels VALUES ('channel',7,NULL), ('other',7,NULL)`,
		`INSERT INTO users VALUES ('alice',1,NULL),('bob',1,NULL)`,
		`INSERT INTO tenant_members VALUES (7,'alice','active',NULL),(7,'bob','active',NULL)`,
		`INSERT INTO im_channel_web_owners VALUES (7,'channel','alice',NULL),(7,'other','bob',NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	const physical = "local://7/exports/report.pdf"
	ref, err := catalog.Register(context.Background(), 7, physical, interfaces.ResourceRegistration{})
	require.NoError(t, err)
	require.NoError(
		t,
		catalog.Bind(
			context.Background(),
			ref,
			types.ResourceOwnerMessage,
			"message",
			types.ResourceRelationArtifact,
		),
	)
	require.NoError(
		t,
		db.Exec(`INSERT INTO message_artifacts VALUES (?,?,?,NULL)`, "message", "session", ref).Error,
	)
	check := catalog.(access.OctoArtifactAccess)
	ctx := func(tenant uint64, user string) context.Context {
		c := types.WithCaller(context.Background(), types.Caller{TenantID: tenant, UserID: user})
		return types.WithPrincipal(c, types.Principal{Type: types.PrincipalWebUser, ID: user})
	}
	for _, path := range []string{ref, physical} {
		for _, tc := range []struct {
			name         string
			ctx          context.Context
			match, allow bool
		}{
			{"bound", ctx(7, "alice"), true, true},
			{"other channel", ctx(7, "bob"), true, false},
			{"other tenant", ctx(8, "alice"), false, false},
			{
				"no principal", types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "alice"}),
				true, false,
			},
			{"api key", types.WithTenantAPIKeyScope(ctx(7, "alice"), types.TenantAPIKeyScope{}), true, false},
		} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				matched, allowed, err := check.CheckOctoArtifactAccess(
					tc.ctx,
					types.CallerFromContext(tc.ctx).TenantID,
					path,
				)
				if tc.name == "other tenant" && path == physical {
					require.ErrorIs(t, err, access.ErrForbidden)
					return
				}
				require.NoError(t, err)
				require.Equal(t, tc.match, matched)
				require.Equal(t, tc.allow, allowed)
			})
		}
	}
	// A backend wrapper may reach an unwrapped registration's physical file.
	for _, wrapped := range []string{
		"storage://backend-a/local://7/exports/report.pdf", "storage://backend-a/local://7/exports/./report.pdf",
	} {
		matched, allowed, err := check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, wrapped)
		require.NoError(t, err)
		require.True(t, matched)
		require.False(t, allowed)
		matched, allowed, err = check.CheckOctoArtifactAccess(ctx(7, "alice"), 7, wrapped)
		require.NoError(t, err)
		require.True(t, matched)
		require.True(t, allowed)
	}
	// The underlying backend reads the same bytes, but the origin check must
	// stop the unbound user before that read.
	local := filesvc.NewLocalFileService(t.TempDir(), "")
	localPath, err := local.SaveBytes(context.Background(), []byte("private IM"), 7, "same-file.pdf", false)
	require.NoError(t, err)
	localRef, err := catalog.Register(context.Background(), 7, localPath, interfaces.ResourceRegistration{})
	require.NoError(t, err)
	require.NoError(
		t,
		catalog.Bind(
			context.Background(),
			localRef,
			types.ResourceOwnerMessage,
			"message",
			types.ResourceRelationArtifact,
		),
	)
	require.NoError(
		t,
		db.Exec(`INSERT INTO message_artifacts VALUES (?,?,?,NULL)`, "message", "session", localRef).Error,
	)
	wrappedPath := "storage://backend-a/" + localPath
	matchedByGuard, err := access.CheckOctoFile(ctx(7, "bob"), catalog, 7, wrappedPath)
	require.ErrorIs(t, err, access.ErrForbidden)
	require.True(t, matchedByGuard)
	reader, err := filesvc.NewBackendScopedFileService("backend-a", local).
		GetFile(context.Background(), wrappedPath)
	require.NoError(t, err)
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, "private IM", string(data))
	// Different local prefix depths can still address one physical path.
	matchedNested, allowedNested, err := check.CheckOctoArtifactAccess(
		ctx(7, "bob"),
		7,
		"storage://nested/local://7/7/exports/report.pdf",
	)
	require.NoError(t, err)
	require.True(t, matchedNested)
	require.False(t, allowedNested)
	matchedNested, allowedNested, err = check.CheckOctoArtifactAccess(
		ctx(7, "alice"),
		7,
		"storage://nested/local://7/7/exports/report.pdf",
	)
	require.NoError(t, err)
	require.True(t, matchedNested)
	require.True(t, allowedNested)
	// An exact Web registration must not hide an IM registration at the
	// same local locator in another backend.
	webShadow, err := catalog.Register(
		context.Background(),
		7,
		"storage://web-backend/local://7/exports/report.pdf",
		interfaces.ResourceRegistration{},
	)
	require.NoError(t, err)
	matchedShadow, allowedShadow, err := check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, webShadow)
	require.NoError(t, err)
	require.True(t, matchedShadow)
	require.False(t, allowedShadow)
	matchedShadow, allowedShadow, err = check.CheckOctoArtifactAccess(
		ctx(7, "alice"),
		7,
		"storage://web-backend/local://7/exports/report.pdf",
	)
	require.NoError(t, err)
	require.True(t, matchedShadow)
	require.True(t, allowedShadow)
	// If potentially equivalent resources belong to different channels, each
	// resource must independently have a channel grant.
	require.NoError(t, db.Exec(`INSERT INTO sessions VALUES ('second-session',7)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO messages VALUES ('second-message','second-session')`).Error)
	require.NoError(
		t,
		db.Exec(`INSERT INTO im_channel_sessions VALUES ('second-session',7,'other','octo',NULL)`).Error,
	)
	secondRef, err := catalog.Register(
		context.Background(),
		7,
		"storage://second-backend/local://7/exports/report.pdf",
		interfaces.ResourceRegistration{},
	)
	require.NoError(t, err)
	require.NoError(
		t,
		catalog.Bind(
			context.Background(),
			secondRef,
			types.ResourceOwnerMessage,
			"second-message",
			types.ResourceRelationArtifact,
		),
	)
	require.NoError(
		t,
		db.Exec(
			`INSERT INTO message_artifacts VALUES (?,?,?,NULL)`,
			"second-message",
			"second-session",
			secondRef,
		).Error,
	)
	_, allowedShadow, err = check.CheckOctoArtifactAccess(ctx(7, "alice"), 7, physical)
	require.NoError(t, err)
	require.False(t, allowedShadow)
	require.NoError(t, db.Exec(`INSERT INTO im_channel_web_owners VALUES (7,'other','alice',NULL)`).Error)
	_, allowedShadow, err = check.CheckOctoArtifactAccess(ctx(7, "alice"), 7, physical)
	require.NoError(t, err)
	require.True(t, allowedShadow)
	_, err = catalog.Release(context.Background(), secondRef, types.ResourceOwnerMessage, "second-message")
	require.NoError(t, err)
	// A lexically different local path resolves to the same registered
	// resource before the channel ownership decision.
	for _, alias := range []string{
		"local://7/exports/./report.pdf", "local://7/exports//report.pdf", "local://7/exports/sub/../report.pdf",
	} {
		matched, allowed, err := check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, alias)
		require.NoError(t, err)
		require.True(t, matched)
		require.False(t, allowed)
		matched, allowed, err = check.CheckOctoArtifactAccess(ctx(7, "alice"), 7, alias)
		require.NoError(t, err)
		require.True(t, matched)
		require.True(t, allowed)
	}
	_, _, err = check.CheckOctoArtifactAccess(ctx(7, "alice"), 7, "local://7/exports/../../7/report.pdf")
	require.ErrorIs(t, err, access.ErrForbidden)
	_, _, err = check.CheckOctoArtifactAccess(ctx(7, "alice"), 7, "local://8/exports/./report.pdf")
	require.ErrorIs(t, err, access.ErrForbidden)
	// Even an exact historical registry hit cannot authorize a path that
	// leaves the original tenant root.
	_, err = catalog.Register(
		context.Background(),
		7,
		"local://7/../8/secret.pdf",
		interfaces.ResourceRegistration{},
	)
	require.NoError(t, err)
	_, _, err = check.CheckOctoArtifactAccess(ctx(7, "alice"), 7, "local://7/../8/secret.pdf")
	require.ErrorIs(t, err, access.ErrForbidden)
	webRef, err := catalog.Register(
		context.Background(),
		7,
		"local://7/exports/web.pdf",
		interfaces.ResourceRegistration{},
	)
	require.NoError(t, err)
	require.NotEmpty(t, webRef)
	matched, _, err := check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, "local://7/exports/./web.pdf")
	require.NoError(t, err)
	require.False(t, matched)
	matched, _, err = check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, "local://7/exports/sub/../web.pdf")
	require.NoError(t, err)
	require.False(t, matched)
	matched, _, err = check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, "s3://bucket/7/exports/./report.pdf")
	require.NoError(t, err)
	require.True(t, matched) // no registration or origin: generic proxy fails closed
	scopedRef, err := catalog.Register(
		context.Background(),
		7,
		"storage://backend-a/local://7/exports/scoped.pdf",
		interfaces.ResourceRegistration{},
	)
	require.NoError(t, err)
	require.NoError(
		t,
		catalog.Bind(
			context.Background(),
			scopedRef,
			types.ResourceOwnerMessage,
			"message",
			types.ResourceRelationArtifact,
		),
	)
	require.NoError(
		t,
		db.Exec(`INSERT INTO message_artifacts VALUES (?,?,?,NULL)`, "message", "session", scopedRef).Error,
	)
	matched, allowed, err := check.CheckOctoArtifactAccess(
		ctx(7, "bob"),
		7,
		"storage://backend-a/local://7/exports/./scoped.pdf",
	)
	require.NoError(t, err)
	require.True(t, matched)
	require.False(t, allowed)
	matched, allowed, err = check.CheckOctoArtifactAccess(
		ctx(7, "alice"),
		7,
		"storage://backend-a/local://7/exports/./scoped.pdf",
	)
	require.NoError(t, err)
	require.True(t, matched)
	require.True(t, allowed)
	// Unwrapped access must also see an IM resource registered with a backend.
	matched, allowed, err = check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, "local://7/exports/scoped.pdf")
	require.NoError(t, err)
	require.True(t, matched)
	require.False(t, allowed)
	matched, allowed, err = check.CheckOctoArtifactAccess(ctx(7, "alice"), 7, "local://7/exports/scoped.pdf")
	require.NoError(t, err)
	require.True(t, matched)
	require.True(t, allowed)
	matched, allowed, err = check.CheckOctoArtifactAccess(
		ctx(7, "bob"),
		7,
		"storage://backend-b/local://7/exports/./scoped.pdf",
	)
	require.NoError(t, err)
	require.True(t, matched)
	require.False(t, allowed)
	// A historical noncanonical stored path still uses its original hash first.
	legacy := "local://7/exports/./legacy.pdf"
	legacyRef, err := catalog.Register(context.Background(), 7, legacy, interfaces.ResourceRegistration{})
	require.NoError(t, err)
	require.NoError(
		t,
		catalog.Bind(
			context.Background(),
			legacyRef,
			types.ResourceOwnerMessage,
			"message",
			types.ResourceRelationArtifact,
		),
	)
	require.NoError(
		t,
		db.Exec(`INSERT INTO message_artifacts VALUES (?,?,?,NULL)`, "message", "session", legacyRef).Error,
	)
	matched, allowed, err = check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, "local://7/exports/legacy.pdf")
	require.NoError(t, err)
	require.True(t, matched)
	require.False(t, allowed)
	matched, allowed, err = check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, legacy)
	require.NoError(t, err)
	require.True(t, matched)
	require.False(t, allowed)
	msg := &types.Message{
		ID:            "message",
		SessionID:     "session",
		AgentTenantID: 7,
		Content:       ref,
		Artifacts:     types.MessageArtifacts{{URL: ref}},
	}
	_, err = access.AuthorizeMessageFile(ctx(7, "bob"), msg, ref, nil, catalog, access.MessageKBShareAuthorizer{})
	require.ErrorIs(t, err, access.ErrForbidden)
	_, err = access.AuthorizeMessageFile(
		ctx(7, "alice"),
		msg,
		ref,
		nil,
		catalog,
		access.MessageKBShareAuthorizer{},
	)
	require.NoError(t, err)
	_, err = access.ResolveMessageArtifact(ctx(7, "bob"), msg, 0, nil, catalog, access.MessageKBShareAuthorizer{})
	require.ErrorIs(t, err, access.ErrForbidden)
	// A separate, live KB binding retains its own authorization route.
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}, &types.Knowledge{}))
	require.NoError(t, db.Create(&types.KnowledgeBase{ID: "kb", TenantID: 7}).Error)
	require.NoError(
		t,
		db.Create(&types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", Type: "file"}).Error,
	)
	require.NoError(
		t,
		catalog.Bind(
			context.Background(),
			ref,
			types.ResourceOwnerKnowledge,
			"doc",
			types.ResourceRelationSourceFile,
		),
	)
	bob := ctx(7, "bob")
	grant := &access.KBAccess{
		KnowledgeBase:     &types.KnowledgeBase{ID: "kb", TenantID: 7},
		Caller:            types.CallerFromContext(bob),
		EffectiveTenantID: 7,
		Permission:        types.OrgRoleViewer,
	}
	_, err = access.ResolveKBFile(bob, grant, "kb", ref, catalog, catalog.(interfaces.KBResourceLookup))
	require.NoError(t, err)
	require.NoError(
		t,
		db.Exec(`UPDATE im_channel_web_owners SET deleted_at=CURRENT_TIMESTAMP WHERE web_user_id='alice'`).Error,
	)
	matched, allowed, err = check.CheckOctoArtifactAccess(ctx(7, "alice"), 7, ref)
	require.NoError(t, err)
	require.True(t, matched)
	require.False(t, allowed)
	require.NoError(t, db.Exec(`DELETE FROM message_artifacts`).Error)
	matched, _, err = check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, ref)
	require.NoError(t, err)
	require.True(t, matched) // an IM resource binding still protects a missing row
	require.NoError(t, db.Exec(`DROP TABLE im_channel_sessions`).Error)
	_, _, err = check.CheckOctoArtifactAccess(ctx(7, "bob"), 7, ref)
	require.Error(t, err)
}
