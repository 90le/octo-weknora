package octo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func durableAdapter(t *testing.T) (*Adapter, *im.IncomingMessage) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "inbox.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&Inbox{}); err != nil {
		t.Fatal(err)
	}
	a, err := NewAdapter("bf_test", "bot")
	if err != nil {
		t.Fatal(err)
	}
	a.db, a.channelID, a.tenantID = db, "channel", 1
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return &im.ExecutionScope{KnowledgeBaseIDs: []string{"kb"}}, nil
	}
	msg, err := a.Normalize(&wire.Message{ID: "2098355867442221056", Sender: "user", Channel: "group____2098355867442221056", ChannelType: 5, Payload: json.RawMessage(`{"type":1,"content":"Original question"}`)})
	if err != nil {
		t.Fatal(err)
	}
	msg.UserName = "Question owner"
	return a, msg
}

func inboxRow(t *testing.T, a *Adapter, id string) Inbox {
	t.Helper()
	var row Inbox
	if err := a.db.Where("tenant_id = ? AND channel_id = ? AND message_id = ?", a.tenantID, a.channelID, id).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestInboxDeduplicatesAcrossAdapterRestart(t *testing.T) {
	a, msg := durableAdapter(t)
	ctx := context.Background()
	calls := 0
	handler := func(ctx context.Context, m *im.IncomingMessage) error {
		calls++
		a.ExecutionFinished(ctx, m)
		return nil
	}
	if err := a.accept(ctx, msg, handler); err != nil {
		t.Fatal(err)
	}
	restarted := *a
	if err := restarted.accept(ctx, msg, handler); err != nil {
		t.Fatal(err)
	}
	restarted.recoverInbox(ctx, handler)
	if calls != 1 {
		t.Fatalf("completed input executed %d times", calls)
	}
	if row := inboxRow(t, a, msg.MessageID); row.State != "finished" {
		t.Fatalf("terminal state = %s", row.State)
	}
	otherChannel := *a
	otherChannel.channelID = "another-channel"
	if err := otherChannel.accept(ctx, msg, func(context.Context, *im.IncomingMessage) error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("inbox keys collided across channels")
	}
}

func TestInboxDeliveryRetryRestoresReplyWithoutRerunningHandler(t *testing.T) {
	a, msg := durableAdapter(t)
	ctx := context.Background()
	var mu sync.Mutex
	var clients []string
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Client string `json:"client_msg_no"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid send request")
		}
		mu.Lock()
		clients = append(clients, payload.Client)
		shouldFail := fail
		mu.Unlock()
		if shouldFail {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"message_id":"2098355867442221058"}`))
	}))
	defer server.Close()
	a.api.base = server.URL
	if err := a.accept(ctx, msg, func(context.Context, *im.IncomingMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := a.SendReply(ctx, msg, &im.ReplyMessage{Content: "Saved answer", IsFinal: true}); err == nil {
		t.Fatal("failed delivery reported success")
	}
	a.ExecutionFinished(ctx, msg)
	row := inboxRow(t, a, msg.MessageID)
	if row.State != "reply_pending" || row.Reply != "Saved answer" || row.Attempts != 1 || row.ErrorCode != "send_failed" {
		t.Fatalf("pending delivery lost: %+v", row)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	restarted := *a
	handlerCalls := 0
	restarted.recoverInbox(ctx, func(context.Context, *im.IncomingMessage) error { handlerCalls++; return nil })
	row = inboxRow(t, a, msg.MessageID)
	if row.State != "delivered" || row.Attempts != 2 || row.ErrorCode != "" || handlerCalls != 0 {
		t.Fatalf("unsafe retry state=%s attempts=%d error=%s handler=%d", row.State, row.Attempts, row.ErrorCode, handlerCalls)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(clients) != 2 || clients[0] == "" || clients[0] != clients[1] {
		t.Fatal("delivery retry changed platform idempotency key")
	}
}

func TestInboxDeniedInputIsNotRetainedAndRevokedReplyIsNotSent(t *testing.T) {
	a, msg := durableAdapter(t)
	ctx := context.Background()
	denied := func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return nil, im.ErrScopeDenied
	}
	allowed := a.policy
	a.policy = denied
	handlerCalls := 0
	if err := a.accept(ctx, msg, func(context.Context, *im.IncomingMessage) error { handlerCalls++; return nil }); err != nil {
		t.Fatal(err)
	}
	var count int64
	a.db.Model(&Inbox{}).Count(&count)
	if count != 0 || handlerCalls != 0 {
		t.Fatal("denied input was retained or dispatched")
	}
	a.policy = allowed
	if err := a.accept(ctx, msg, func(context.Context, *im.IncomingMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := a.inboxQuery(ctx, msg.MessageID).Updates(map[string]any{"state": "reply_pending", "reply": "Private knowledge"}).Error; err != nil {
		t.Fatal(err)
	}
	a.policy = denied
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("revoked answer disclosed to platform")
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()
	a.api.base = server.URL
	a.recoverInbox(ctx, func(context.Context, *im.IncomingMessage) error { handlerCalls++; return nil })
	if row := inboxRow(t, a, msg.MessageID); row.State != "ignored" || row.ErrorCode != "authorization_changed" {
		t.Fatalf("revoked reply not terminal: %+v", row)
	}
	if handlerCalls != 0 {
		t.Fatal("revoked reply reran handler")
	}
}

func TestInboxDispatchFailureIsObservable(t *testing.T) {
	a, msg := durableAdapter(t)
	err := a.accept(context.Background(), msg, func(context.Context, *im.IncomingMessage) error { return errors.New("queue unavailable") })
	if err == nil {
		t.Fatal("dispatch failure lost")
	}
	if row := inboxRow(t, a, msg.MessageID); row.State != "failed" || row.ErrorCode != "dispatch_failed" {
		t.Fatalf("failure not retained: %+v", row)
	}
}

func TestInboxInterruptedWorkIsNotMarkedCompleted(t *testing.T) {
	a, msg := durableAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.accept(ctx, msg, func(context.Context, *im.IncomingMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cancel()
	a.ExecutionFinished(ctx, msg)
	row := inboxRow(t, a, msg.MessageID)
	if row.State != "failed" || row.ErrorCode != "execution_interrupted" {
		t.Fatalf("interrupted work claimed completion: %+v", row)
	}
}

func TestInboxRecoveryPagesWithoutCrossTenantOrChannelDispatch(t *testing.T) {
	a, msg := durableAdapter(t)
	ctx := context.Background()
	calls := 0
	for i := 0; i < 25; i++ {
		copy := *msg
		copy.MessageID = fmt.Sprintf("restore-%03d", i)
		raw, _ := json.Marshal(copy)
		if err := a.db.Create(&Inbox{ChannelID: a.channelID, TenantID: a.tenantID, MessageID: copy.MessageID, Input: string(raw), State: "queued"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(msg)
	for _, row := range []Inbox{{ChannelID: a.channelID, TenantID: 2, MessageID: "foreign-tenant", Input: string(raw), State: "queued"}, {ChannelID: "foreign-channel", TenantID: 1, MessageID: "foreign-channel", Input: string(raw), State: "queued"}} {
		if err := a.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	a.recoverInbox(ctx, func(ctx context.Context, m *im.IncomingMessage) error {
		calls++
		a.ExecutionFinished(ctx, m)
		return nil
	})
	if calls != 25 {
		t.Fatalf("recovered %d messages, want only 25 admitted rows across pages", calls)
	}
	var untouched int64
	a.db.Model(&Inbox{}).Where("state = ?", "queued").Count(&untouched)
	if untouched != 2 {
		t.Fatalf("foreign inbox rows changed: %d queued", untouched)
	}
}

func TestInboxRecoveryMakesCorruptionAndRetryExhaustionVisible(t *testing.T) {
	a, msg := durableAdapter(t)
	raw, _ := json.Marshal(msg)
	for _, row := range []Inbox{{ChannelID: a.channelID, TenantID: 1, MessageID: msg.MessageID, Input: string(raw), State: "reply_pending", Attempts: 3, Reply: "stored"}, {ChannelID: a.channelID, TenantID: 1, MessageID: "broken", Input: "{", State: "processing"}} {
		if err := a.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("exhausted delivery sent again") }))
	defer server.Close()
	a.api.base = server.URL
	a.recoverInbox(context.Background(), func(context.Context, *im.IncomingMessage) error { t.Error("invalid recovery dispatched"); return nil })
	for _, expect := range []struct{ id, code string }{{msg.MessageID, "delivery_retry_exhausted"}, {"broken", "invalid_stored_input"}} {
		row := inboxRow(t, a, expect.id)
		if row.State != "failed" || row.ErrorCode != expect.code {
			t.Fatalf("terminal recovery state missing: %+v", row)
		}
	}
}
