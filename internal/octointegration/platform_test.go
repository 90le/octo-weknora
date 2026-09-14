package octointegration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func platformFixture(t *testing.T, f http.HandlerFunc) *platformClient {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	p := newPlatformClient()
	p.baseURL = server.URL
	return p
}

func TestPlatformIdentityMatchingAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"raw", `{"group_no":"g","name":"Real group","status":1}`, true},
		{"wrapped", `{"data":{"group_no":"g","name":"Real group","status":1}}`, true},
		{"wrong group", `{"group_no":"other","name":"Other group","status":1}`, false},
		{"inactive", `{"group_no":"g","name":"Group","status":0}`, false},
		{"missing status", `{"group_no":"g","name":"Group"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer bf_test" {
					t.Error("missing bearer")
				}
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := p.scope(context.Background(), "bf_test", Scope{GroupID: "g"})
			if (err == nil) != tc.valid {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPlatformThreadIDsStayStrings(t *testing.T) {
	const id = "2098228705884114944"
	p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bot/groups/g/threads/"+id {
			t.Error(r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"group_no": "g", "short_id": id, "name": "Topic", "status": 1})
	})
	v, err := p.scope(context.Background(), "bf_test", Scope{GroupID: "g", SubareaID: id})
	if err != nil || v.SubareaID != id {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestPlatformRedirectAndErrorBodiesAreNotTrusted(t *testing.T) {
	called := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer dest.Close()
	p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, http.StatusFound) })
	_, err := p.scope(context.Background(), "bf_secret", Scope{GroupID: "g"})
	if err == nil || called || strings.Contains(err.Error(), "bf_secret") {
		t.Fatalf("unsafe redirect: %v", err)
	}
}

func TestNativeRoleMappingFailsClosedForUnknownBots(t *testing.T) {
	n := func(v int) *int { return &v }
	for _, tc := range []struct {
		name string
		m    platformMember
		can  bool
		role string
	}{
		{"owner", platformMember{UID: "u", Robot: n(0), Role: n(1)}, true, "owner"},
		{"admin", platformMember{UID: "u", Robot: n(0), Role: n(2)}, true, "admin"},
		{"member", platformMember{UID: "u", Robot: n(0), Role: n(0)}, false, "member"},
		{"bot with human role", platformMember{UID: "b", Robot: n(1), Role: n(1)}, false, "member"},
		{"explicit bot admin", platformMember{UID: "b", Robot: n(1), Role: n(0), BotAdmin: n(1)}, true, "admin"},
		{"missing robot", platformMember{UID: "u", Role: n(1)}, false, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := resolveMember(tc.m)
			if r.CanManageGroup != tc.can || r.Role != tc.role {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestConnectionsEncryptedAndSyncRetainsLastGoodName(t *testing.T) {
	s, ctx := testStore(t), context.Background()
	t.Setenv("SYSTEM_AES_KEY", "")
	if err := s.PutConnection(ctx, 1, "bot", "bf_test"); !errors.Is(err, ErrEncryption) {
		t.Fatalf("plaintext accepted: %v", err)
	}
	t.Setenv("SYSTEM_AES_KEY", "01234567890123456789012345678901")
	if err := s.PutConnection(ctx, 1, "bot", "bf_test"); err != nil {
		t.Fatal(err)
	}
	var c Connection
	s.db.First(&c)
	if c.Token == "bf_test" || !strings.HasPrefix(c.Token, "enc:v1:") {
		t.Fatal("credential not encrypted")
	}
	encoded, _ := json.Marshal(c)
	if strings.Contains(string(encoded), "token") || strings.Contains(string(encoded), "bf_test") {
		t.Fatal("credential leaked")
	}
	if _, _, err := s.connection(ctx, 2, "bot"); err == nil {
		t.Fatal("cross-tenant token read")
	}
	fail := false
	p := platformFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "private upstream detail", 403)
			return
		}
		_, _ = w.Write([]byte(`{"group_no":"g","name":"Platform name","status":1}`))
	})
	r, err := s.CreateVerified(ctx, Scope{TenantID: 1, AccountID: "bot", GroupID: "g", DisplayName: "Forged"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.DisplayName != "Platform name" || r.NameSource != "octo" || r.VerifiedAt == nil {
		t.Fatalf("%+v", r)
	}
	if err := s.Update(ctx, 1, r.ID, "Forged", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("platform name overwritten: %v", err)
	}
	fail = true
	if _, err := s.SyncName(ctx, 1, r.ID, p); !errors.Is(err, ErrPlatform) {
		t.Fatalf("%v", err)
	}
	after, err := s.Get(ctx, 1, r.ID)
	if err != nil || after.DisplayName != r.DisplayName || after.SyncStatus != "error" || !after.VerifiedAt.Equal(*r.VerifiedAt) {
		t.Fatalf("last good name lost: %+v %v", after, err)
	}
	if err := s.PutConnection(ctx, 1, "bot", "bf_rotated"); err != nil {
		t.Fatal(err)
	}
	after, err = s.Get(ctx, 1, r.ID)
	if err != nil || after.SyncStatus != "needs_refresh" {
		t.Fatalf("rotation kept verification: %+v %v", after, err)
	}
}
