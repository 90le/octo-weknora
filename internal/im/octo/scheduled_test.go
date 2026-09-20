package octo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/utils"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func scheduledAdapter(t *testing.T) (*Adapter, octobusiness.ReportSchedule) {
	t.Helper()
	t.Setenv("SYSTEM_AES_KEY", "01234567890123456789012345678901")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "schedule-policy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&octointegration.Scope{}, &octointegration.Binding{}, &octointegration.Connection{}, &octointegration.KnowledgeManagementGrant{}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`CREATE TABLE im_channels (id TEXT, tenant_id INTEGER, enabled BOOLEAN, credentials TEXT, knowledge_base_id TEXT, updated_at DATETIME, deleted_at DATETIME)`, `CREATE TABLE knowledge_bases (id TEXT,tenant_id INTEGER,deleted_at DATETIME)`, `INSERT INTO knowledge_bases VALUES ('kb',1,NULL)`, `INSERT INTO im_channels VALUES ('channel',1,1,'{"account_id":"account","bot_uid":"bot","allowed_dm_uids":["owner"]}','kb',CURRENT_TIMESTAMP,NULL)`} {
		if err = db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	secret, err := utils.EncryptAESGCM("bf_test", utils.GetAESKey())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&octointegration.Connection{TenantID: 1, AccountID: "account", Token: secret}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, scope := range []octointegration.Scope{{ID: "parent", TenantID: 1, AccountID: "account", GroupID: "group", DisplayName: "Parent", NameSource: "octo", SyncStatus: "verified", VerifiedAt: &now}, {ID: "child", TenantID: 1, AccountID: "account", GroupID: "group", SubareaID: "2098355867442221056", DisplayName: "Child", NameSource: "octo", SyncStatus: "verified", VerifiedAt: &now}} {
		if err = db.Create(&scope).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Create(&octointegration.Binding{TenantID: 1, ScopeID: "child", KnowledgeBaseID: "kb"}).Error; err != nil {
		t.Fatal(err)
	}
	a, err := NewAdapter("bf_test", "bot")
	if err != nil {
		t.Fatal(err)
	}
	a.db, a.channelID, a.tenantID = db, "channel", 1
	return a, octobusiness.ReportSchedule{ID: "schedule", TenantID: 1, ChannelID: "channel", ScopeID: "child", RecipientType: "source", RunKey: "run-1"}
}

func TestScheduledChildScopeUsesNativeShortID(t *testing.T) {
	a, schedule := scheduledAdapter(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bot/groups/group/threads/2098355867442221056" {
			t.Errorf("unexpected scope verification: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"group_no":"group","short_id":"2098355867442221056","status":1}`))
	}))
	defer server.Close()
	a.api.base = server.URL
	p, err := a.reportPrincipal(context.Background(), schedule)
	if err != nil || p.SubareaID != "2098355867442221056" || len(p.KnowledgeBaseIDs) != 1 {
		t.Fatalf("valid native child denied: %+v %v", p, err)
	}
}

func TestScheduledPrivateChildRequiresCurrentParentMembership(t *testing.T) {
	a, schedule := scheduledAdapter(t)
	schedule.RecipientType = "private"
	schedule.RecipientUID = "owner"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/groups/group/threads/2098355867442221056":
			_, _ = w.Write([]byte(`{"group_no":"group","short_id":"2098355867442221056","status":1}`))
		case "/v1/bot/groups/group/members":
			_, _ = w.Write([]byte(`[]`))
		case "/v1/bot/groups/group/threads/2098355867442221056/members":
			_, _ = w.Write([]byte(`[{"uid":"owner"}]`))
		default:
			t.Errorf("unauthorized report delivery attempted: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	if err := a.DeliverReport(context.Background(), schedule, &octobusiness.Report{GeneratedAt: time.Now()}, nil); err == nil {
		t.Fatal("removed parent member received child report")
	}
}

func TestScheduledDeliveryDeniesDifferentTenantAndDisabledChannel(t *testing.T) {
	a, schedule := scheduledAdapter(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("denied schedule reached platform") }))
	defer server.Close()
	a.api.base = server.URL
	other := schedule
	other.TenantID = 2
	if _, err := a.ReportContext(context.Background(), other); err == nil {
		t.Fatal("cross-tenant schedule accepted")
	}
	other = schedule
	other.ChannelID = "another"
	if _, err := a.ReportContext(context.Background(), other); err == nil {
		t.Fatal("cross-channel schedule accepted")
	}
	if err := a.db.Exec("UPDATE im_channels SET enabled=0").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverReport(context.Background(), schedule, &octobusiness.Report{}, nil); err == nil {
		t.Fatal("disabled channel sent report")
	}
}
