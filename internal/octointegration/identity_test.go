package octointegration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProbeReturnsIdentityWithoutRegistrationSecrets(t *testing.T) {
	p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/bot/register" || r.Header.Get("Authorization") != "Bearer bf_private" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"robot_id":"bot-1","name":"Knowledge bot","owner_uid":"owner-1","im_token":"private-im-secret","ws_url":"wss://private.invalid"}}`))
	})
	identity, err := p.identity(context.Background(), "bf_private")
	if err != nil || identity.BotUID != "bot-1" || identity.Name != "Knowledge bot" || identity.OwnerUID != "owner-1" {
		t.Fatalf("identity: %+v error: %v", identity, err)
	}
	raw, _ := json.Marshal(identity)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "token") || strings.Contains(string(raw), "ws_url") {
		t.Fatalf("secret exposed in public identity: %s", raw)
	}
}

func TestProbeRejectsEmptyIdentityAndInvalidCredential(t *testing.T) {
	calls := 0
	p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{"name":"unverified"}`)) })
	for _, token := range []string{"", "bf_", "bf_has space", "uk_not_a_bot"} {
		if _, err := p.identity(context.Background(), token); err == nil {
			t.Fatalf("invalid token accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid input sent upstream")
	}
	if _, err := p.identity(context.Background(), "bf_test"); err == nil {
		t.Fatal("missing native UID accepted")
	}
}

func TestAvailableScopesVerifyParentAndPreserveLongThreadIDs(t *testing.T) {
	p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/groups":
			_, _ = w.Write([]byte(`[{"group_no":"g","name":"Actual group"},{"group_no":"gone","name":"Deleted","status":0}]`))
		case "/v1/bot/groups/g":
			_, _ = w.Write([]byte(`{"group_no":"g","name":"Actual group","status":1}`))
		case "/v1/bot/groups/g/threads":
			if r.URL.Query().Get("page_index") != "2" {
				t.Fatal("pagination lost")
			}
			_, _ = w.Write([]byte(`{"list":[{"short_id":"2098228705884114944","name":"Actual subarea"}]}`))
		default:
			http.NotFound(w, r)
		}
	})
	groups, err := p.availableScopes(context.Background(), "bf_test", "", 1)
	if err != nil || len(groups) != 1 || groups[0].Name != "Actual group" {
		t.Fatalf("groups: %+v %v", groups, err)
	}
	children, err := p.availableScopes(context.Background(), "bf_test", "g", 2)
	if err != nil || len(children) != 1 || children[0].SubareaID != "2098228705884114944" || children[0].GroupID != "g" {
		t.Fatalf("children: %+v %v", children, err)
	}
	if _, err := p.availableScopes(context.Background(), "bf_test", "../other", 1); err == nil {
		t.Fatal("unsafe parent accepted")
	}
}
