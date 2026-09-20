package octo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
)

const deleteOP = "OP-12345678-1234-1234-1234-123456789abc"

func deletionFixture(t *testing.T) (*Adapter, *im.IncomingMessage, *bool, *bool, *[]string) {
	t.Helper()
	a, msg := durableAdapter(t)
	if err := a.db.AutoMigrate(&octobusiness.Proposal{}, &octointegration.Scope{}, &octointegration.Binding{}, &octointegration.Connection{}, &octointegration.KnowledgeManagementGrant{}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE im_channels (id TEXT,tenant_id INTEGER,enabled BOOLEAN,credentials TEXT,knowledge_base_id TEXT,updated_at DATETIME,deleted_at DATETIME)`,
		`INSERT INTO im_channels VALUES ('channel',1,1,'{"account_id":"account","bot_uid":"bot"}','',CURRENT_TIMESTAMP,NULL)`,
		`CREATE TABLE knowledge_bases (id TEXT,tenant_id INTEGER,deleted_at DATETIME)`,
		`INSERT INTO knowledge_bases VALUES ('kb',1,CURRENT_TIMESTAMP)`,
	} {
		if err := a.db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := a.db.Create(&octointegration.Scope{ID: "scope", TenantID: 1, AccountID: "account", GroupID: "group", NameSource: "octo", SyncStatus: "verified", VerifiedAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.db.Create(&octointegration.Connection{TenantID: 1, AccountID: "account", Token: "snapshot"}).Error; err != nil {
		t.Fatal(err)
	}
	proposal := octobusiness.Proposal{ID: deleteOP, TenantID: 1, AccountID: "account", ChannelID: "channel", ScopeID: "scope", UserID: "user", Action: "delete_kb", KnowledgeBaseID: "kb", Status: "completed", SourceMessageID: "previous", IdempotencyKey: "delete", Payload: types.JSON(`{"action":"delete_kb","knowledge_base_id":"kb"}`), Result: types.JSON(`{"deletion_requested":true,"knowledge_base_id":"kb"}`)}
	if err := a.db.Create(&proposal).Error; err != nil {
		t.Fatal(err)
	}
	member, fail := true, false
	sent := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/groups/group/members":
			if member {
				fmt.Fprint(w, `[{"uid":"user","robot":0,"role":1}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case "/v1/bot/sendMessage":
			var request struct {
				Payload struct {
					Content string `json:"content"`
				} `json:"payload"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			sent = append(sent, request.Payload.Content)
			if fail {
				w.WriteHeader(503)
			} else {
				fmt.Fprint(w, `{"message_id":"2098355867442221060"}`)
			}
		default:
			t.Errorf("unexpected API %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	a.api.base = srv.URL
	a.policy = runtimePolicy(a.db, a, "channel", 1, "account", "snapshot")
	a.receiptPolicy = scopedRuntimePolicy(a.db, a, "channel", 1, "account", "snapshot", true)
	msg.ChatType = im.ChatTypeGroup
	msg.ChatID = "group"
	msg.Content = "确认 " + deleteOP
	msg.Extra["octo_channel_type"] = "2"
	msg.Extra["octo_channel_id"] = "group"
	msg.Extra["octo_group_id"] = "group"
	msg.Extra["octo_subarea_id"] = ""
	msg.Extra["octo_addressed"] = "true"
	msg.Extra["octo_command_text"] = msg.Content
	return a, msg, &member, &fail, &sent
}

func TestCompletedDeletionLastKBReceiptDoesNotInvokeAgentOrRepeatBusiness(t *testing.T) {
	a, msg, _, _, sent := deletionFixture(t)
	ctx := context.Background()
	if _, err := a.AuthorizeExecution(ctx, nil, msg); !errors.Is(err, im.ErrScopeDenied) {
		t.Fatal("last KB loss opened normal Agent scope")
	}
	calls := 0
	handler := func(context.Context, *im.IncomingMessage) error { calls++; return nil }
	for i := 0; i < 2; i++ {
		if err := a.accept(ctx, msg, handler); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 || len(*sent) != 1 || !strings.HasSuffix((*sent)[0], deletionReceiptText) {
		t.Fatalf("unsafe receipt calls=%d sent=%v", calls, *sent)
	}
	if row := inboxRow(t, a, msg.MessageID); row.State != "delivered" || row.Reply != deletionReceiptText {
		t.Fatalf("wrong durable receipt %+v", row)
	}
	// A second late framework completion cannot overwrite or resend the receipt.
	if err := a.SendReply(ctx, msg, &im.ReplyMessage{Content: "private model content"}); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 {
		t.Fatal("delivered receipt resent")
	}
}

func TestCompletedDeletionReceiptRetryPersistsOnlyFixedText(t *testing.T) {
	a, msg, _, fail, sent := deletionFixture(t)
	ctx := context.Background()
	*fail = true
	if err := a.accept(ctx, msg, func(context.Context, *im.IncomingMessage) error { t.Fatal("Agent invoked"); return nil }); err == nil {
		t.Fatal("send failure hidden")
	}
	row := inboxRow(t, a, msg.MessageID)
	if row.State != "reply_pending" || row.Reply != deletionReceiptText {
		t.Fatalf("receipt not retryable %+v", row)
	}
	*fail = false
	restarted, _ := NewAdapter("bf_test", "bot")
	restarted.db, restarted.channelID, restarted.tenantID, restarted.api = a.db, a.channelID, a.tenantID, a.api
	restarted.policy = a.policy
	restarted.receiptPolicy = a.receiptPolicy
	restarted.recoverInbox(ctx, func(context.Context, *im.IncomingMessage) error { t.Fatal("recovery reran Agent"); return nil })
	if row = inboxRow(t, a, msg.MessageID); row.State != "delivered" || row.Attempts != 2 {
		t.Fatalf("retry failed %+v", row)
	}
	if len(*sent) != 2 || (*sent)[0] != (*sent)[1] {
		t.Fatal("retry contents changed")
	}
}

func TestDeletionReceiptRejectsForeignAndNonCompletedOperations(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"foreign-user", `UPDATE octo_knowledge_proposals SET user_id='other'`},
		{"foreign-tenant", `UPDATE octo_knowledge_proposals SET tenant_id=2`},
		{"foreign-account", `UPDATE octo_knowledge_proposals SET account_id='other'`},
		{"foreign-channel", `UPDATE octo_knowledge_proposals SET channel_id='other'`},
		{"foreign-scope", `UPDATE octo_knowledge_proposals SET scope_id='other'`},
		{"pending", `UPDATE octo_knowledge_proposals SET status='pending'`},
		{"other-action", `UPDATE octo_knowledge_proposals SET action='rename'`},
		{"no-result", `UPDATE octo_knowledge_proposals SET result='{}'`},
		{"target-mismatch", `UPDATE octo_knowledge_proposals SET result='{"deletion_requested":true,"knowledge_base_id":"other"}'`},
		{"not-deleted", `UPDATE knowledge_bases SET deleted_at=NULL`},
		{"rotated-token", `UPDATE octo_connections SET token='rotated'`},
		{"disabled-channel", `UPDATE im_channels SET enabled=0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, msg, _, _, sent := deletionFixture(t)
			if err := a.db.Exec(tc.query).Error; err != nil {
				t.Fatal(err)
			}
			if a.deletionReceipt(context.Background(), msg) != nil {
				t.Fatal("unproven receipt authorized")
			}
			if len(*sent) != 0 {
				t.Fatal("sent during validation")
			}
		})
	}
}

func TestDeletionReceiptStillRequiresNativeCommandAndCurrentMembership(t *testing.T) {
	a, msg, member, _, sent := deletionFixture(t)
	ctx := context.Background()
	for _, bad := range []string{"请确认 " + deleteOP, "取消 " + deleteOP, "确认"} {
		msg.Extra["octo_command_text"] = bad
		if a.deletionReceipt(ctx, msg) != nil {
			t.Fatal("nonconfirmation authorized")
		}
	}
	msg.Extra["octo_command_text"] = "确认 " + deleteOP
	*member = false
	if err := a.accept(ctx, msg, func(context.Context, *im.IncomingMessage) error { t.Fatal("revoked member invoked Agent"); return nil }); err != nil {
		t.Fatal(err)
	}
	var count int64
	a.db.Model(&Inbox{}).Count(&count)
	if count != 0 || len(*sent) != 0 {
		t.Fatal("revoked sender retained or answered")
	}
}

func TestDeletionReceiptFinishesOriginalExecutionAfterLastGrantRemoved(t *testing.T) {
	a, msg, _, _, sent := deletionFixture(t)
	ctx := context.Background()
	if err := a.db.Exec("UPDATE knowledge_bases SET deleted_at=NULL").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.db.Create(&octointegration.Binding{TenantID: 1, ScopeID: "scope", KnowledgeBaseID: "kb"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.db.Create(&octointegration.KnowledgeManagementGrant{TenantID: 1, ScopeID: "scope", KnowledgeBaseID: "kb"}).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := a.accept(ctx, msg, func(ctx context.Context, m *im.IncomingMessage) error {
		calls++
		if err := a.db.Exec("UPDATE knowledge_bases SET deleted_at=CURRENT_TIMESTAMP").Error; err != nil {
			return err
		}
		if err := a.db.Exec("DELETE FROM octo_scope_bindings").Error; err != nil {
			return err
		}
		if err := a.db.Exec("DELETE FROM octo_scope_knowledge_grants").Error; err != nil {
			return err
		}
		// The native framework may decline its ordinary response after the action
		// revokes the scope. Its completion hook still delivers only the receipt.
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		a.ExecutionFinished(cancelled, m)
		return nil
	})
	if err != nil || calls != 1 || len(*sent) != 1 || inboxRow(t, a, msg.MessageID).Reply != deletionReceiptText {
		t.Fatalf("original completion lost: %v calls=%d sent=%v", err, calls, *sent)
	}
}

func TestDeletionPendingReceiptIsBlockedAfterNativeMembershipRevocation(t *testing.T) {
	a, msg, member, fail, sent := deletionFixture(t)
	ctx := context.Background()
	*fail = true
	_ = a.accept(ctx, msg, func(context.Context, *im.IncomingMessage) error { t.Fatal("Agent invoked"); return nil })
	*member = false
	*fail = false
	a.recoverInbox(ctx, func(context.Context, *im.IncomingMessage) error { t.Fatal("recovery invoked Agent"); return nil })
	if len(*sent) != 1 || inboxRow(t, a, msg.MessageID).State != "ignored" {
		t.Fatal("revoked member received queued receipt")
	}
}
