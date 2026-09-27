package octo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRuntimePolicyEnforcesMembersBindingsAndRotation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "policy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err = db.AutoMigrate(&octointegration.Scope{}, &octointegration.Binding{}, &octointegration.Connection{}, &octointegration.KnowledgeManagementGrant{}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE TABLE im_channels (id TEXT, tenant_id INTEGER, enabled BOOLEAN, credentials TEXT, knowledge_base_id TEXT, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE knowledge_bases (id TEXT,tenant_id INTEGER,deleted_at DATETIME)`,
		`INSERT INTO knowledge_bases VALUES ('kb',1,NULL)`,
		`INSERT INTO im_channels VALUES ('channel',1,1,'{"account_id":"account","bot_uid":"bot","allowed_dm_uids":["owner"]}','kb',CURRENT_TIMESTAMP,NULL)`,
	} {
		if err = db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	scope := octointegration.Scope{ID: "scope", TenantID: 1, AccountID: "account", GroupID: "group", DisplayName: "group", NameSource: "octo", SyncStatus: "verified", VerifiedAt: &now}
	if err = db.Create(&scope).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&octointegration.Connection{TenantID: 1, AccountID: "account", Token: "snapshot"}).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&octointegration.Binding{TenantID: 1, ScopeID: scope.ID, KnowledgeBaseID: "kb"}).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/bot/user/info" {
			fmt.Fprint(w, `{"uid":"owner","name":"验收用户"}`)
			return
		}
		fmt.Fprint(w, `[{"uid":"human","robot":0},{"uid":"untrusted_bot","robot":1}]`)
	}))
	defer server.Close()
	a, _ := NewAdapter("bf_test", "bot")
	a.api.base = server.URL
	a.policy = runtimePolicy(db, a, "channel", 1, "account", "snapshot")
	msg := &im.IncomingMessage{Platform: Platform, UserID: "human", ChatType: im.ChatTypeGroup, ChatID: "group", Extra: map[string]string{"octo_channel_id": "group", "octo_group_id": "group", "octo_addressed": "true"}}
	got, err := a.AuthorizeExecution(context.Background(), nil, msg)
	if err != nil || len(got.KnowledgeBaseIDs) != 1 || got.KnowledgeBaseIDs[0] != "kb" {
		t.Fatalf("valid scope: %v %v", got, err)
	}
	if got.AllowPublicWeb {
		t.Fatal("new group unexpectedly authorized public web")
	}
	if err = db.Model(&octointegration.Scope{}).Where("id = ?", scope.ID).Update("allow_public_web", true).Error; err != nil {
		t.Fatal(err)
	}
	webEnabled, err := a.AuthorizeExecution(context.Background(), nil, msg)
	if err != nil || !webEnabled.AllowPublicWeb || im.ExecutionScopeFingerprint(got) == im.ExecutionScopeFingerprint(webEnabled) {
		t.Fatalf("policy grant did not reauthorize: %+v %v", webEnabled, err)
	}
	child := octointegration.Scope{ID: "child", TenantID: 1, AccountID: "account", GroupID: "group", SubareaID: "123", DisplayName: "child", NameSource: "octo", SyncStatus: "verified", VerifiedAt: &now, InheritParent: true}
	if err = db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	childMsg := *msg
	childMsg.ChatID = "group____123"
	childMsg.Extra = map[string]string{"octo_channel_id": "group____123", "octo_group_id": "group", "octo_subarea_id": "123", "octo_addressed": "true"}
	childScope, err := a.AuthorizeExecution(context.Background(), nil, &childMsg)
	if err != nil || childScope.AllowPublicWeb || len(childScope.KnowledgeBaseIDs) != 1 {
		t.Fatalf("child inherited parent's web grant or lost KB read inheritance: %+v %v", childScope, err)
	}
	if err = db.Model(&octointegration.Scope{}).Where("id = ?", child.ID).Update("allow_public_web", true).Error; err != nil {
		t.Fatal(err)
	}
	childEnabled, err := a.AuthorizeExecution(context.Background(), nil, &childMsg)
	if err != nil || !childEnabled.AllowPublicWeb {
		t.Fatalf("explicit child web grant not applied: %+v %v", childEnabled, err)
	}
	if err = db.Model(&octointegration.Scope{}).Where("id = ?", scope.ID).Update("allow_public_web", false).Error; err != nil {
		t.Fatal(err)
	}
	webRevoked, err := a.AuthorizeExecution(context.Background(), nil, msg)
	if err != nil || webRevoked.AllowPublicWeb {
		t.Fatalf("web revocation not applied to queued reauthorization: %+v %v", webRevoked, err)
	}
	if err = db.Exec("UPDATE im_channels SET updated_at='2020-01-01'").Error; err != nil {
		t.Fatal(err)
	}
	afterMetadata, err := a.AuthorizeExecution(context.Background(), nil, msg)
	if err != nil || afterMetadata.Revision != got.Revision {
		t.Fatal("metadata refresh invalidated conversation")
	}
	msg.UserID = "untrusted_bot"
	if _, err = a.AuthorizeExecution(context.Background(), nil, msg); err == nil {
		t.Fatal("ordinary Bot admitted")
	}
	msg.UserID = "unknown"
	if _, err = a.AuthorizeExecution(context.Background(), nil, msg); err == nil {
		t.Fatal("non-member admitted")
	}
	msg.UserID = "human"
	msg.Extra["octo_group_id"] = "other"
	if _, err = a.AuthorizeExecution(context.Background(), nil, msg); err == nil {
		t.Fatal("forged group admitted")
	}
	msg.Extra["octo_group_id"] = "group"
	if err = db.Where("scope_id = ?", scope.ID).Delete(&octointegration.Binding{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = a.AuthorizeExecution(context.Background(), nil, msg); err == nil {
		t.Fatal("unbound scope fell back to all KBs")
	}
	msg.ChatType = im.ChatTypeDirect
	msg.UserID = "owner"
	dmScope, err := a.AuthorizeExecution(context.Background(), nil, msg)
	if err != nil {
		t.Fatal("explicit DM denied")
	}
	if dmScope.SenderName != "验收用户" {
		t.Fatal("native DM name was not resolved")
	}
	if dmScope.AllowPublicWeb {
		t.Fatal("group public web policy leaked into private message")
	}
	if err = db.Exec(`UPDATE octo_connections SET token='rotated'`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = a.AuthorizeExecution(context.Background(), nil, msg); err == nil {
		t.Fatal("stale connection used")
	}
}
