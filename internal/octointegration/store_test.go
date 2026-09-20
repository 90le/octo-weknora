package octointegration

import (
	"context"
	"errors"
	"github.com/Tencent/WeKnora/internal/types"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "octo.db")+"?_foreign_keys=on"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := db.DB()
	t.Cleanup(func() { _ = raw.Close() })
	if err := db.AutoMigrate(&types.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE knowledge_bases (id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, deleted_at TIMESTAMP)").Error; err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sqlite/000017_octo_scopes.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(migration)).Error; err != nil {
		t.Fatal(err)
	}
	next, err := os.ReadFile("../../migrations/sqlite/000018_octo_connections.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(next)).Error; err != nil {
		t.Fatal(err)
	}
	grants, err := os.ReadFile("../../migrations/sqlite/000023_octo_knowledge_grants.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(string(grants)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO knowledge_bases(id, tenant_id) VALUES ('kb-a', 1), ('kb-b', 2)").Error; err != nil {
		t.Fatal(err)
	}
	return NewStore(db)
}

func createScope(t *testing.T, s *Store, tenant uint64, account, group, sub string, inherit bool) *Scope {
	t.Helper()
	r, err := s.Create(context.Background(), Scope{TenantID: tenant, AccountID: account, GroupID: group, SubareaID: sub, DisplayName: "configured name", InheritParent: inherit})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestScopeIsolationAndInheritance(t *testing.T) {
	s, ctx := testStore(t), context.Background()
	parent := createScope(t, s, 1, "bot", "group", "", false)
	child := createScope(t, s, 1, "bot", "group", "topic", true)
	other := createScope(t, s, 1, "other-bot", "group", "", false)
	createScope(t, s, 2, "bot", "group", "", false)
	if err := s.SetBinding(ctx, 1, parent.ID, "kb-a", true); err != nil {
		t.Fatal(err)
	}
	got, err := s.Effective(ctx, 1, child.ID)
	if err != nil || len(got) != 1 || !got[0].Inherited || got[0].FromScopeID != parent.ID {
		t.Fatalf("inheritance: %+v %v", got, err)
	}
	if err := s.SetBinding(ctx, 1, child.ID, "kb-a", true); err != nil {
		t.Fatal(err)
	}
	got, err = s.Effective(ctx, 1, child.ID)
	if err != nil || len(got) != 1 || got[0].Inherited {
		t.Fatalf("duplicate direct/inherited binding: %+v %v", got, err)
	}
	if err := s.SetBinding(ctx, 1, child.ID, "kb-a", false); err != nil {
		t.Fatal(err)
	}
	got, err = s.Effective(ctx, 1, other.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("cross-account leak: %+v %v", got, err)
	}
	if _, err := s.Effective(ctx, 2, child.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-tenant read: %v", err)
	}
	if err := s.SetBinding(ctx, 1, parent.ID, "kb-b", true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-tenant bind: %v", err)
	}
	if err := s.Update(ctx, 1, child.ID, "independent", false); err != nil {
		t.Fatal(err)
	}
	got, err = s.Effective(ctx, 1, child.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("independent scope fell back: %+v %v", got, err)
	}
}

func TestBindingUnbindAndDeletedAsset(t *testing.T) {
	s, ctx := testStore(t), context.Background()
	scope := createScope(t, s, 1, "bot", "group", "", false)
	for i := 0; i < 2; i++ {
		if err := s.SetBinding(ctx, 1, scope.ID, "kb-a", true); err != nil {
			t.Fatal(err)
		}
	}
	uses, err := s.Uses(ctx, 1, "kb-a")
	if err != nil || len(uses) != 1 {
		t.Fatalf("idempotent bind: %+v %v", uses, err)
	}
	if err := s.SetBinding(ctx, 1, scope.ID, "kb-a", false); err != nil {
		t.Fatal(err)
	}
	var count int64
	s.db.Table("knowledge_bases").Where("id = ?", "kb-a").Count(&count)
	if count != 1 {
		t.Fatal("unbind deleted knowledge base")
	}
	if err := s.SetBinding(ctx, 1, scope.ID, "kb-a", true); err != nil {
		t.Fatal(err)
	}
	s.db.Exec("UPDATE knowledge_bases SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'kb-a'")
	got, err := s.Effective(ctx, 1, scope.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted KB remained effective: %+v %v", got, err)
	}
}

func TestScopeIdentityAndValidation(t *testing.T) {
	s, ctx := testStore(t), context.Background()
	if _, err := s.Create(ctx, Scope{TenantID: 1, AccountID: "bot", GroupID: "g", SubareaID: "orphan", DisplayName: "x"}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("orphan accepted: %v", err)
	}
	r := createScope(t, s, 1, "bot", "900719925474099312345", "", false)
	if err := s.Update(ctx, 1, r.ID, "renamed", false); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, 1, r.ID)
	if err != nil || after.GroupID != r.GroupID || after.NameSource != "configured" {
		t.Fatalf("identity changed: %+v %v", after, err)
	}
	if err := s.Update(ctx, 1, r.ID, "renamed", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("root inheritance accepted: %v", err)
	}
	if _, err := s.Create(ctx, *r); !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("duplicate identity: %v", err)
	}
	if _, err := s.List(ctx, 0, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing tenant: %v", err)
	}
}

func TestScopeMutationRollsBackWhenAuditFails(t *testing.T) {
	s, ctx := testStore(t), context.Background()
	if err := s.db.Exec("DROP TABLE audit_logs").Error; err != nil {
		t.Fatal(err)
	}
	_, err := s.Create(ctx, Scope{TenantID: 1, AccountID: "bot", GroupID: "g", DisplayName: "group"})
	if err == nil {
		t.Fatal("expected audit error")
	}
	rows, err := s.List(ctx, 1, 0)
	if err != nil || len(rows) != 0 {
		t.Fatalf("mutation committed without audit: %+v %v", rows, err)
	}
}
