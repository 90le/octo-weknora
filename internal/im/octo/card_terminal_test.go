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
)

func TestConfirmationCardTerminalUpdateAndRetry(t *testing.T) {
	a, p, _ := confirmationFixture(t)
	clientID := confirmationCardClientID(p.ID)
	update := map[string]any{"client_msg_no": clientID, "updated_at": time.Now().Add(-time.Minute)}
	if err := a.db.Model(&confirmationCard{}).Where("channel_id = ? AND proposal_id = ?", a.channelID, p.ID).
		Updates(update).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.db.Model(&octobusiness.Proposal{}).Where("id = ?", p.ID).
		Updates(map[string]any{
			"status": "cancelled", "updated_at": time.Now(), "result": `{"action":"create_kb","name":"synthetic"}`,
		}).Error; err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bot/message/edit" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			return
		}
		var body struct {
			Channel string `json:"channel_id"`
			Content string `json:"content_edit"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Channel != "group" || !strings.Contains(body.Content, "已取消") ||
			!strings.Contains(body.Content, "synthetic") ||
			strings.Contains(body.Content, "Action.Submit") {
			t.Error("terminal card lost its scope or still contains action buttons")
		}
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	a.api.base = server.URL
	a.reconcileConfirmationCards(context.Background())
	var card confirmationCard
	if err := a.db.Where("channel_id = ? AND proposal_id = ?", a.channelID, p.ID).First(&card).Error; err != nil ||
		card.State != "sent" {
		t.Fatalf("failed edit closed the card: %s %v", card.State, err)
	}
	a.reconcileConfirmationCards(context.Background())
	if calls.Load() != 1 {
		t.Fatal("failed edit was not throttled")
	}
	if err := a.db.Model(&confirmationCard{}).Where("channel_id = ? AND proposal_id = ?", a.channelID, p.ID).
		Update("updated_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	a.reconcileConfirmationCards(context.Background())
	a.reconcileConfirmationCards(context.Background())
	if err := a.db.Where("channel_id = ? AND proposal_id = ?", a.channelID, p.ID).First(&card).Error; err != nil ||
		card.State != "closed" ||
		calls.Load() != 2 {
		t.Fatalf("terminal card not reconciled once: state=%s calls=%d err=%v", card.State, calls.Load(), err)
	}
}

func TestConfirmationCardTerminalRespectsExpiryAuthorizationAndHistory(t *testing.T) {
	for _, scenario := range []string{"legacy", "unexpired", "revoked", "expired", "uncertain"} {
		t.Run(scenario, func(t *testing.T) {
			a, p, _ := confirmationFixture(t)
			id := confirmationCardClientID(p.ID)
			if scenario == "legacy" {
				id = "legacy-client-id"
			}
			update := map[string]any{"client_msg_no": id, "updated_at": time.Now().Add(-time.Minute)}
			if err := a.db.Model(&confirmationCard{}).Where("channel_id = ? AND proposal_id = ?", a.channelID, p.ID).
				Updates(update).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "expired" {
				if err := a.db.Model(&octobusiness.Proposal{}).Where("id = ?", p.ID).
					Update("expires_at", time.Now().Add(-2*time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "uncertain" || scenario == "revoked" || scenario == "legacy" {
				status := "cancelled"
				if scenario == "uncertain" {
					status = "failed"
				}
				if err := a.db.Model(&octobusiness.Proposal{}).Where("id = ?", p.ID).
					Update("status", status).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "revoked" {
				a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
					return nil, im.ErrScopeDenied
				}
			}
			var edits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				edits.Add(1)
				var body struct {
					Content string `json:"content_edit"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if scenario == "uncertain" && !strings.Contains(body.Content, "未确认完成") {
					t.Error("ambiguous failure reported as certain")
				}
				if scenario == "expired" && !strings.Contains(body.Content, "已过期") {
					t.Error("expired status missing")
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			a.api.base = server.URL
			a.reconcileConfirmationCards(context.Background())
			want := int32(0)
			if scenario == "expired" || scenario == "uncertain" {
				want = 1
			}
			if edits.Load() != want {
				t.Fatalf("unexpected edits=%d", edits.Load())
			}
		})
	}
}

func TestConfirmationTerminalDeletionIsNotClaimedFullyCleaned(t *testing.T) {
	p := &octobusiness.Proposal{Action: "delete_kb", Status: "completed"}
	if text := confirmationTerminalText(p, time.Now()); text != deletionReceiptText {
		t.Fatalf("deletion status: %q", text)
	}
}

func TestConfirmationTerminalDeletionRequiresCurrentMembership(t *testing.T) {
	a, msg, _, _, _ := deletionFixture(t)
	a.business = octobusiness.NewService(a.db, nil, nil)
	if err := a.db.AutoMigrate(&confirmationCard{}); err != nil {
		t.Fatal(err)
	}
	msg.MessageID = "previous"
	input, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.Create(&Inbox{
		TenantID: a.tenantID, ChannelID: a.channelID, MessageID: msg.MessageID,
		Input: string(input), Authority: "{}", State: "delivered",
	}).Error; err != nil {
		t.Fatal(err)
	}
	card := confirmationCard{
		TenantID: a.tenantID, ChannelID: a.channelID, ProposalID: deleteOP,
		MessageID: "987654", ClientMsgNo: confirmationCardClientID(deleteOP), State: "sent",
	}
	if err := a.db.Create(&card).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.db.Model(&confirmationCard{}).Where("proposal_id = ?", deleteOP).
		Update("updated_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	var member atomic.Bool
	var edits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/groups/group/members":
			if member.Load() {
				_, _ = w.Write([]byte(`[{"uid":"user","robot":0,"role":1}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case "/v1/bot/message/edit":
			var body struct {
				Content string `json:"content_edit"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !strings.Contains(body.Content, deletionReceiptText) {
				t.Error("deletion was described as fully cleaned")
			}
			edits.Add(1)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	a.reconcileConfirmationCards(context.Background())
	if edits.Load() != 0 {
		t.Fatal("card edited after membership revoked")
	}
	member.Store(true)
	if err := a.db.Model(&confirmationCard{}).Where("proposal_id = ?", deleteOP).
		Update("updated_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	a.reconcileConfirmationCards(context.Background())
	if edits.Load() != 1 {
		t.Fatal("verified deletion receipt did not update the original card")
	}
}
