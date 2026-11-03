package octo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

func fixtureEventKey(e storedCardEvent) string {
	return cardEventKey(
		cardAction{
			EventID:     e.EventID,
			MessageID:   e.MessageID,
			ChannelID:   e.TargetID,
			ChannelType: byte(e.TargetType),
			OperatorUID: e.OperatorUID,
			ActionID:    e.ActionID,
		},
	)
}

func fixtureCardAction(id int, operator string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"event_id": id, "event_type": "card_action",
		"event_data": map[string]any{
			"message_id": "987654", "channel_id": "group", "channel_type": 2,
			"action_id": "knowledge_confirm", "operator_uid": operator,
		},
	})
	return raw
}

func confirmationFixture(t *testing.T) (*Adapter, octobusiness.Proposal, *atomic.Int32) {
	t.Helper()
	a, _ := cardStoreFixture(t)
	if err := a.db.AutoMigrate(&octobusiness.Proposal{}, &types.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.Exec("CREATE TABLE tenants (id BIGINT PRIMARY KEY, deleted_at DATETIME)").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.db.Exec("INSERT INTO tenants (id) VALUES (?)", a.tenantID).Error; err != nil {
		t.Fatal(err)
	}
	scope := &im.ExecutionScope{
		AccountID:              "account",
		ScopeID:                "scope",
		Revision:               "r",
		KnowledgeBaseIDs:       []string{"kb"},
		ManageKnowledgeBaseIDs: []string{"kb"},
		CanManageScope:         true,
	}
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return scope, nil
	}
	a.business = octobusiness.NewService(a.db, nil, nil)
	calls := &atomic.Int32{}
	a.business.Management = func(
		ctx context.Context, p octobusiness.Principal, _ octobusiness.ManagementInput, preview bool,
	) (map[string]any, error) {
		if tenant, ok := types.TenantInfoFromContext(ctx); !ok || tenant.ID != p.TenantID {
			t.Error("tenant configuration missing")
		}
		if !p.CanManageScope {
			return nil, octobusiness.ErrDenied
		}
		if !preview {
			calls.Add(1)
		}
		return map[string]any{"action": "create_kb"}, nil
	}
	msg := typingMessage("2", "group")
	msg.MessageID = "octo:bot:100"
	msg.ChatType = im.ChatTypeGroup
	msg.Extra["octo_addressed"] = "true"
	raw, _ := json.Marshal(msg)
	source := Inbox{
		TenantID: a.tenantID, ChannelID: a.channelID, MessageID: msg.MessageID,
		Input: string(raw), Authority: encodeAuthority(scope), State: "delivered",
	}
	if err := a.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	p := octobusiness.Proposal{
		ID:              "OP-" + uuid.NewString(),
		TenantID:        a.tenantID,
		AccountID:       "account",
		ChannelID:       a.channelID,
		ScopeID:         "scope",
		UserID:          msg.UserID,
		SourceMessageID: msg.MessageID,
		Action:          "create_kb",
		Payload:         types.JSON(`{"action":"create_kb","name":"synthetic"}`),
		Result:          types.JSON(`{"action":"create_kb"}`),
		IdempotencyKey:  uuid.NewString(),
		Status:          "pending",
		ExpiresAt:       time.Now().Add(time.Minute),
	}
	if err := a.db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	binding := confirmationCard{
		TenantID: a.tenantID, ChannelID: a.channelID, ProposalID: p.ID,
		MessageID: "987654", State: "sent",
	}
	if err := a.db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bot/sendMessage" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"message_id":"987655"}`))
	}))
	t.Cleanup(server.Close)
	a.api.base = server.URL
	return a, p, calls
}

func TestCardConfirmationBindsIdentityAndNeverReplays(t *testing.T) {
	a, p, calls := confirmationFixture(t)
	ctx := context.Background()
	route := a.confirmationHandler(
		func(context.Context, *im.IncomingMessage) error { t.Error("card went to model"); return nil },
	)
	event := storedCardEvent{
		TenantID:    a.tenantID,
		ChannelID:   a.channelID,
		EventID:     1,
		MessageID:   "987654",
		TargetID:    "group",
		TargetType:  2,
		OperatorUID: "other",
		ActionID:    "knowledge_confirm",
		State:       "waiting",
	}
	event.EventKey = fixtureEventKey(event)
	if err := a.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := a.acceptConfirmationEvent(ctx, event, route); !ok || err != nil {
		t.Fatalf("other operator: %v %v", ok, err)
	}
	if calls.Load() != 0 {
		t.Fatal("other operator executed change")
	}
	event.EventID = 2
	event.OperatorUID = p.UserID
	event.TargetID = "wrong"
	event.EventKey = fixtureEventKey(event)
	if err := a.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := a.acceptConfirmationEvent(ctx, event, route); !ok || err != nil {
		t.Fatalf("other scope: %v %v", ok, err)
	}
	if calls.Load() != 0 {
		t.Fatal("other conversation executed change")
	}
	event.EventID = 3
	event.TargetID = "group"
	event.EventKey = fixtureEventKey(event)
	if err := a.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := a.acceptConfirmationEvent(ctx, event, route); !ok || err != nil {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("execution count %d", calls.Load())
	}
	if ok, err := a.acceptConfirmationEvent(ctx, event, route); !ok || err != nil {
		t.Fatalf("replay: %v %v", ok, err)
	}
	event.EventID = 4
	event.EventKey = fixtureEventKey(event)
	if err := a.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := a.acceptConfirmationEvent(ctx, event, route); !ok || err != nil {
		t.Fatalf("repeat click: %v %v", ok, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("replayed mutation: %d", calls.Load())
	}
	var saved octobusiness.Proposal
	if err := a.db.First(&saved, "id = ?", p.ID).Error; err != nil || saved.Status != "completed" {
		t.Fatalf("proposal status: %s %v", saved.Status, err)
	}
}

func TestCardCancellationUsesCurrentDisplayNameWithoutEmptyQuote(t *testing.T) {
	a, p, calls := confirmationFixture(t)
	scope := &im.ExecutionScope{
		AccountID: "account", ScopeID: "scope", Revision: "r", SenderName: "丘😀",
		KnowledgeBaseIDs: []string{"kb"}, ManageKnowledgeBaseIDs: []string{"kb"}, CanManageScope: true,
	}
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return scope, nil
	}
	var payload struct {
		Content string          `json:"content"`
		Reply   json.RawMessage `json:"reply"`
		Mention struct {
			UIDs     []string `json:"uids"`
			Entities []struct {
				Length int `json:"length"`
			} `json:"entities"`
		} `json:"mention"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal(request.Payload, &payload); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"message_id":"987655"}`))
	}))
	defer server.Close()
	a.api.base = server.URL
	event := storedCardEvent{
		TenantID: a.tenantID, ChannelID: a.channelID, EventID: 42, MessageID: "987654",
		TargetID: "group", TargetType: 2, OperatorUID: p.UserID, ActionID: "knowledge_cancel", State: "waiting",
	}
	event.EventKey = fixtureEventKey(event)
	if err := a.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	route := a.confirmationHandler(func(context.Context, *im.IncomingMessage) error {
		t.Error("card cancellation went to model")
		return nil
	})
	if ok, err := a.acceptConfirmationEvent(context.Background(), event, route); !ok || err != nil {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if !strings.HasPrefix(payload.Content, "@丘😀 ") || len(payload.Reply) != 0 ||
		len(payload.Mention.UIDs) != 1 || payload.Mention.UIDs[0] != p.UserID ||
		len(payload.Mention.Entities) != 1 || payload.Mention.Entities[0].Length != 4 || calls.Load() != 0 {
		t.Fatalf("incorrect cancellation presentation: %+v", payload)
	}
}

func TestCardConfirmationCancelExpiryAndRevocation(t *testing.T) {
	for _, scenario := range []string{"cancel", "expired", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			a, p, calls := confirmationFixture(t)
			if scenario == "expired" {
				err := a.db.Model(&octobusiness.Proposal{}).Where("id = ?", p.ID).
					Update("expires_at", time.Now().Add(-time.Minute)).Error
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "revoked" {
				a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
					return nil, im.ErrScopeDenied
				}
			}
			e := storedCardEvent{
				TenantID:    a.tenantID,
				ChannelID:   a.channelID,
				EventID:     5,
				MessageID:   "987654",
				TargetID:    "group",
				TargetType:  2,
				OperatorUID: p.UserID,
				ActionID:    "knowledge_confirm",
				State:       "waiting",
			}
			if scenario == "cancel" {
				e.ActionID = "knowledge_cancel"
			}
			e.EventKey = fixtureEventKey(e)
			if err := a.db.Create(&e).Error; err != nil {
				t.Fatal(err)
			}
			route := a.confirmationHandler(
				func(context.Context, *im.IncomingMessage) error { return fmt.Errorf("unexpected normal dispatch") },
			)
			if ok, err := a.acceptConfirmationEvent(context.Background(), e, route); !ok || err != nil {
				t.Fatalf("event: %v %v", ok, err)
			}
			if calls.Load() != 0 {
				t.Fatal("executed cancelled, expired or revoked operation")
			}
			var saved octobusiness.Proposal
			if err := a.db.First(&saved, "id = ?", p.ID).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "cancel" && saved.Status != "cancelled" {
				t.Fatalf("status %s", saved.Status)
			}
		})
	}
}

func TestCardEventPollingKeepsUnknownAndFindsLateCollidingEvents(t *testing.T) {
	a, p, calls := confirmationFixture(t)
	var polls atomic.Int32
	var acknowledgements atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/events":
			var request struct {
				EventID uint64 `json:"event_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.EventID != 0 {
				t.Error("completed pass did not restart")
			}
			events := []json.RawMessage{
				json.RawMessage(`{"event_id":11,"event_type":"other"}`),
				fixtureCardAction(11, "other"), fixtureCardAction(11, "other-2"),
			}
			if polls.Add(1) > 1 {
				events = append(events, fixtureCardAction(9, p.UserID))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": events})
		case "/v1/bot/sendMessage":
			_, _ = w.Write([]byte(`{"message_id":"987655"}`))
		default:
			acknowledgements.Add(1)
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	route := a.confirmationHandler(
		func(context.Context, *im.IncomingMessage) error { t.Error("card went to model"); return nil },
	)
	a.pollConfirmationEvents(context.Background(), route)
	a.pollConfirmationEvents(context.Background(), route)
	if calls.Load() != 1 || acknowledgements.Load() != 0 {
		t.Fatalf("mutation=%d acknowledgements=%d", calls.Load(), acknowledgements.Load())
	}
	var count int64
	err := a.db.Model(&storedCardEvent{}).Where("channel_id = ? AND state = ?",
		a.channelID, "processed").Count(&count).Error
	if err != nil || count != 3 {
		t.Fatalf("colliding/late events processed=%d %v", count, err)
	}
}

func TestCardEventPollingDoesNotSkipCollisionAtFullPageBoundary(t *testing.T) {
	a, p, calls := confirmationFixture(t)
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/events":
			var request struct {
				EventID uint64 `json:"event_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			var events []json.RawMessage
			if reads.Add(1) == 1 {
				if request.EventID != 0 {
					t.Error("first scan did not begin at zero")
				}
				for i := 1; i <= 99; i++ {
					events = append(
						events,
						json.RawMessage(fmt.Sprintf(`{"event_id":%d,"event_type":"other"}`, i)),
					)
				}
				events = append(events, fixtureCardAction(100, "other"))
			} else {
				if request.EventID != 99 {
					t.Errorf("crossed boundary: %d", request.EventID)
				}
				events = append(events, fixtureCardAction(100, p.UserID))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": events})
		case "/v1/bot/sendMessage":
			_, _ = w.Write([]byte(`{"message_id":"987655"}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	route := a.confirmationHandler(
		func(context.Context, *im.IncomingMessage) error { t.Error("card went to model"); return nil },
	)
	a.pollConfirmationEvents(context.Background(), route)
	a.pollConfirmationEvents(context.Background(), route)
	var count int64
	err := a.db.Model(&storedCardEvent{}).Where("channel_id = ? AND event_id = ? AND state = ?",
		a.channelID, 100, "processed").Count(&count).Error
	if err != nil || count != 2 || calls.Load() != 1 {
		t.Fatalf("boundary events=%d mutation=%d err=%v", count, calls.Load(), err)
	}
}

func TestCardQueuedWorkRecoversWithoutReplayingMutation(t *testing.T) {
	a, p, calls := confirmationFixture(t)
	e := storedCardEvent{
		TenantID:    a.tenantID,
		ChannelID:   a.channelID,
		EventID:     9,
		MessageID:   "987654",
		TargetID:    "group",
		TargetType:  2,
		OperatorUID: p.UserID,
		ActionID:    "knowledge_confirm",
		State:       "waiting",
	}
	e.EventKey = fixtureEventKey(e)
	if err := a.db.Create(&e).Error; err != nil {
		t.Fatal(err)
	}
	// Represent a crash after durable inbox admission but before execution.
	noop := func(context.Context, *im.IncomingMessage) error { return nil }
	if ok, err := a.acceptConfirmationEvent(context.Background(), e, noop); !ok || err != nil {
		t.Fatalf("admit: %v %v", ok, err)
	}
	if calls.Load() != 0 {
		t.Fatal("executed before recovery")
	}
	route := a.confirmationHandler(
		func(context.Context, *im.IncomingMessage) error { t.Error("card went to model"); return nil },
	)
	a.recoverInbox(context.Background(), route)
	a.recoverInbox(context.Background(), route)
	if calls.Load() != 1 {
		t.Fatalf("recovery mutation count: %d", calls.Load())
	}
}

func TestCardConfirmationRetriesOnlyPreExecutionFailure(t *testing.T) {
	a, p, calls := confirmationFixture(t)
	e := storedCardEvent{
		TenantID:    a.tenantID,
		ChannelID:   a.channelID,
		EventID:     12,
		MessageID:   "987654",
		TargetID:    "group",
		TargetType:  2,
		OperatorUID: p.UserID,
		ActionID:    "knowledge_confirm",
		State:       "waiting",
	}
	e.EventKey = fixtureEventKey(e)
	if err := a.db.Create(&e).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.db.Exec("DROP TABLE tenants").Error; err != nil {
		t.Fatal(err)
	}
	route := a.confirmationHandler(
		func(context.Context, *im.IncomingMessage) error { t.Error("card went to model"); return nil },
	)
	if _, err := a.acceptConfirmationEvent(context.Background(), e, route); err == nil {
		t.Fatal("missing tenant must be retryable")
	}
	if calls.Load() != 0 {
		t.Fatal("execution started without tenant")
	}
	if err := a.db.Exec("CREATE TABLE tenants (id BIGINT PRIMARY KEY, deleted_at DATETIME)").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.db.Exec("INSERT INTO tenants (id) VALUES (?)", a.tenantID).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := a.acceptConfirmationEvent(context.Background(), e, route); !ok || err != nil {
		t.Fatalf("retry: %v %v", ok, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("retry mutation count: %d", calls.Load())
	}
}
