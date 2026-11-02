package octo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestConfirmationCardUsesCompletePreviewAndIsNotRepeated(t *testing.T) {
	a, _ := cardStoreFixture(t)
	a.business = &octobusiness.Service{}
	msg := typingMessage("2", "group")
	msg.MessageID = "octo:bot:123"
	msg.ChatType = im.ChatTypeGroup
	msg.Extra["octo_addressed"] = "true"
	scope := &im.ExecutionScope{AccountID: "account", ScopeID: "scope", Revision: "r"}
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return scope, nil
	}
	if err := a.db.AutoMigrate(&octobusiness.Proposal{}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.Create(&Inbox{
		TenantID: a.tenantID, ChannelID: a.channelID, MessageID: msg.MessageID,
		Input: "{}", Authority: encodeAuthority(scope), State: "delivered",
	}).Error; err != nil {
		t.Fatal(err)
	}
	p := octobusiness.Proposal{
		ID: "OP-123", TenantID: a.tenantID, AccountID: "account",
		ChannelID: a.channelID, ScopeID: "scope", UserID: "user", SourceMessageID: msg.MessageID,
		Action: "rename", Status: "pending", ExpiresAt: time.Now().Add(time.Minute),
		Result: types.JSON(`{"action":"rename","name":"New name","notice":"complete preview"}`),
	}
	if err := a.db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/card/profile":
			_, _ = w.Write([]byte(`{"enabled":true,"profiles":["octo/v2"],"elements":["TextBlock"],
				"limits":{"max_payload_bytes":524288},"config":{"card_enabled":true,"interaction_enabled":true}}`))
		case "/v1/bot/sendMessage":
			sends.Add(1)
			var body struct {
				ChannelID string          `json:"channel_id"`
				Payload   json.RawMessage `json:"payload"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.ChannelID != "group" || !strings.Contains(string(body.Payload), "complete preview") ||
				!strings.Contains(string(body.Payload), "knowledge_confirm") {
				t.Error("card lost its original target or complete preview")
			}
			_, _ = w.Write([]byte(`{"message_id":"987654"}`))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	a.sendConfirmationCards(context.Background(), msg)
	a.sendConfirmationCards(context.Background(), msg)
	if sends.Load() != 1 {
		t.Fatalf("card sent %d times", sends.Load())
	}
	var binding confirmationCard
	if err := a.db.Where("channel_id = ? AND proposal_id = ?", a.channelID, p.ID).First(&binding).Error; err != nil ||
		binding.State != "sent" || binding.MessageID != "987654" {
		t.Fatal("sent card identity was not recorded")
	}
}

func TestConfirmationPreviewRejectsTruncationAndUnsafeControlCharacters(t *testing.T) {
	p := &octobusiness.Proposal{
		ID: "OP-1", Action: "rename",
		Result: types.JSON(`{"action":"rename","name":"complete"}`),
	}
	if _, ok := confirmationPreview(p); !ok {
		t.Fatal("complete preview rejected")
	}
	p.Result = types.JSON(`{"action":"rename","content":"` + strings.Repeat("a", 7000) + `"}`)
	if _, ok := confirmationPreview(p); ok {
		t.Fatal("truncated preview could authorize a change")
	}
	p.Result = types.JSON(`{"action":"rename","name":"\u202Ehidden"}`)
	if _, ok := confirmationPreview(p); ok {
		t.Fatal("visually ambiguous preview was accepted")
	}
}

func TestConfirmationCardRequiresDeclaredInteractionAndPayloadLimit(t *testing.T) {
	p := interactiveCardProfile{Enabled: true, Profiles: []string{"octo/v2"}, Elements: []string{"TextBlock"}}
	p.Config.CardEnabled, p.Config.InteractionEnabled = true, true
	if p.supportsConfirmation() {
		t.Fatal("missing server payload limit accepted")
	}
	p.Limits.MaxPayloadBytes = 4096
	if !p.supportsConfirmation() {
		t.Fatal("supported profile rejected")
	}
	p.Config.InteractionEnabled = false
	if p.supportsConfirmation() {
		t.Fatal("disabled interactions accepted")
	}
}

func TestConfirmationCardDoesNotResendUncertainDelivery(t *testing.T) {
	a, _ := cardStoreFixture(t)
	a.business = &octobusiness.Service{}
	msg := typingMessage("2", "group")
	msg.MessageID, msg.ChatType = "octo:bot:123", im.ChatTypeGroup
	msg.Extra["octo_addressed"] = "true"
	scope := &im.ExecutionScope{AccountID: "account", ScopeID: "scope", Revision: "r"}
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return scope, nil
	}
	if err := a.db.AutoMigrate(&octobusiness.Proposal{}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.Create(&Inbox{TenantID: a.tenantID, ChannelID: a.channelID, MessageID: msg.MessageID, Input: "{}", Authority: encodeAuthority(scope), State: "delivered"}).Error; err != nil {
		t.Fatal(err)
	}
	p := octobusiness.Proposal{ID: "OP-uncertain", TenantID: a.tenantID, AccountID: "account", ChannelID: a.channelID, ScopeID: "scope", UserID: msg.UserID, SourceMessageID: msg.MessageID, Action: "rename", Status: "pending", ExpiresAt: time.Now().Add(time.Minute), Result: types.JSON(`{"action":"rename","name":"synthetic"}`)}
	if err := a.db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/card/profile":
			_, _ = w.Write([]byte(`{"enabled":true,"profiles":["octo/v2"],"elements":["TextBlock"],"limits":{"max_payload_bytes":524288},"config":{"card_enabled":true,"interaction_enabled":true}}`))
		case "/v1/bot/sendMessage":
			sends.Add(1)
			http.Error(w, "ambiguous delivery", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	a.sendConfirmationCards(context.Background(), msg)
	a.sendConfirmationCards(context.Background(), msg)
	if sends.Load() != 1 {
		t.Fatalf("uncertain send attempted %d times", sends.Load())
	}
	var binding confirmationCard
	if err := a.db.Where("channel_id = ? AND proposal_id = ?", a.channelID, p.ID).First(&binding).Error; err != nil || binding.State != "uncertain" {
		t.Fatalf("state %s %v", binding.State, err)
	}
}
