package octointegration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestVerifiedIdentityPageAndRepeatedProbeNeverRegister(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "01234567890123456789012345678901")
	s := testStore(t)
	ctx := context.Background()
	if err := s.putConnection(ctx, 1, "account", "bf_verified", &ConnectionIdentity{BotUID: "bot-1", Name: "Previous", OwnerUID: "owner"}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.connection(ctx, 1, "account")
	reads := 0
	p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/bot/user/info" || r.URL.Query().Get("uid") != "bot-1" {
			t.Errorf("page performed non-read identity operation: %s %s", r.Method, r.URL.String())
			w.WriteHeader(500)
			return
		}
		if r.Header.Get("Authorization") != "Bearer bf_verified" {
			t.Error("wrong credential")
		}
		reads++
		_, _ = w.Write([]byte(`{"uid":"bot-1","name":"Current Bot name"}`))
	})
	h := &Handler{store: s, platform: p}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/identity?uid=attacker", nil)
		c.Set(types.TenantIDContextKey.String(), uint64(1))
		c.Params = gin.Params{{Key: "account_id", Value: "account"}}
		h.ConnectionIdentity(c)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Current Bot name") || strings.Contains(w.Body.String(), "bf_verified") {
			t.Fatalf("read identity: %d %s", w.Code, w.Body.String())
		}
	}
	if _, err := h.probeIdentity(ctx, 1, "bf_verified"); err != nil {
		t.Fatal(err)
	}
	if reads != 3 {
		t.Fatalf("wrong read count %d", reads)
	}
	after, _, _ := s.connection(ctx, 1, "account")
	if after.Token != before.Token || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("opening identity mutated connection credential or revision")
	}
}

func TestMissingIdentityDoesNotTrustClientUIDOrRegisterOnGET(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "01234567890123456789012345678901")
	s := testStore(t)
	ctx := context.Background()
	if err := s.PutConnection(ctx, 1, "account", "bf_legacy"); err != nil {
		t.Fatal(err)
	}
	p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("unverified GET called platform register")
		w.WriteHeader(500)
	})
	h := &Handler{store: s, platform: p}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/identity?bot_uid=pretend", nil)
	c.Set(types.TenantIDContextKey.String(), uint64(1))
	c.Params = gin.Params{{Key: "account_id", Value: "account"}}
	h.ConnectionIdentity(c)
	if w.Code != 409 {
		t.Fatalf("unverified identity accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err := p.readIdentity(ctx, "bf_legacy", nil); !errors.Is(err, ErrIdentityUnverified) {
		t.Fatal(err)
	}
}

func TestVerifiedIdentityCredentialRotationAndCAS(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "01234567890123456789012345678901")
	s := testStore(t)
	ctx := context.Background()
	identity := ConnectionIdentity{BotUID: "bot-1", Name: "Verified"}
	if err := s.putConnection(ctx, 1, "account", "bf_first", &identity); err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.connection(ctx, 1, "account")
	if err := s.RecordVerifiedIdentity(ctx, 1, "account", before.Token, identity); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.connection(ctx, 1, "account")
	if !before.UpdatedAt.Equal(after.UpdatedAt) {
		t.Fatal("metadata update revoked live execution scope")
	}
	if err := s.putConnection(ctx, 1, "account", "bf_first", &identity); err != nil {
		t.Fatal(err)
	}
	after, _, _ = s.connection(ctx, 1, "account")
	if after.Token != before.Token || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("same credential re-save changed active snapshot")
	}
	if err := s.PutConnection(ctx, 1, "account", "bf_rotated"); err != nil {
		t.Fatal(err)
	}
	after, _, _ = s.connection(ctx, 1, "account")
	if after.trustedIdentity() != nil {
		t.Fatal("old identity survived new unverified token")
	}
	if err := s.RecordVerifiedIdentity(ctx, 1, "account", before.Token, identity); err == nil {
		t.Fatal("stale authentication overwrote rotated identity")
	}
	if err := s.RecordVerifiedIdentity(ctx, 2, "account", after.Token, identity); err == nil {
		t.Fatal("cross-tenant identity write")
	}
	if known, err := s.knownIdentity(ctx, 2, "bf_first"); err != nil || known != nil {
		t.Fatal("cross-tenant identity reuse")
	}
}

func TestReadIdentityRejectsDifferentProfileAndNewTokenIsExplicitlyRegistered(t *testing.T) {
	p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"uid":"another","name":"Pretend"}`))
	})
	if _, err := p.readIdentity(context.Background(), "bf_valid", &ConnectionIdentity{BotUID: "expected"}); err == nil {
		t.Fatal("profile UID mismatch accepted")
	}
	registrations := 0
	p = platformFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/bot/register" {
			t.Error("new credential did not use explicit verification")
		}
		registrations++
		_ = json.NewEncoder(w).Encode(map[string]string{"robot_id": "verified-native-bot", "name": "Native"})
	})
	h := &Handler{store: testStore(t), platform: p}
	identity, err := h.probeIdentity(context.Background(), 1, "bf_new")
	if err != nil || identity.BotUID != "verified-native-bot" || registrations != 1 {
		t.Fatalf("new credential identity: %+v %v", identity, err)
	}
}
