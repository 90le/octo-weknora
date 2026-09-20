package octo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
)

func TestGeneratedFileUsesOriginalTargetAndStableDeliveryIdentity(t *testing.T) {
	a, msg := durableAdapter(t)
	ctx := context.Background()
	var mu sync.Mutex
	var sent []struct {
		Channel string `json:"channel_id"`
		Kind    int    `json:"channel_type"`
		Client  string `json:"client_msg_no"`
		Payload struct {
			Type int    `json:"type"`
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"payload"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer bf_test" {
			t.Error("missing official upload credential")
		}
		if r.URL.Path == "/v1/bot/file/upload" {
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Error(err)
			}
			if r.FormValue("type") != "chat" {
				t.Error("unexpected upload purpose")
			}
			_, _ = w.Write([]byte(`{"data":{"url":"https://cdn.deepminer.com.cn/reports/result.html"}}`))
			return
		}
		var body struct {
			Channel string `json:"channel_id"`
			Kind    int    `json:"channel_type"`
			Client  string `json:"client_msg_no"`
			Payload struct {
				Type int    `json:"type"`
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"payload"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid outgoing artifact")
		}
		mu.Lock()
		sent = append(sent, body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"message_id":"2098355867442221058"}`))
	}))
	defer server.Close()
	a.api.base = server.URL
	for i := 0; i < 2; i++ {
		if err := a.SendGeneratedFile(ctx, msg, "report.html", []byte("<h1>Report</h1>"), "text/html"); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("unexpected sends: %d", len(sent))
	}
	for _, body := range sent {
		if body.Channel != msg.Extra["octo_channel_id"] || body.Kind != 5 || body.Payload.Type != 8 || body.Payload.Name != "report.html" {
			t.Fatalf("artifact redirected: %+v", body)
		}
	}
	if sent[0].Client == "" || sent[0].Client != sent[1].Client {
		t.Fatal("retry changed file idempotency key")
	}
}

func TestGeneratedFileRechecksAuthorizationAfterUpload(t *testing.T) {
	a, msg := durableAdapter(t)
	checks := 0
	uploads := 0
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		checks++
		if checks > 1 {
			return nil, im.ErrScopeDenied
		}
		return &im.ExecutionScope{KnowledgeBaseIDs: []string{"kb"}}, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bot/file/upload" {
			t.Error("revoked report sent to chat")
		}
		uploads++
		_, _ = w.Write([]byte(`{"url":"https://cdn.deepminer.com.cn/report.csv"}`))
	}))
	defer server.Close()
	a.api.base = server.URL
	if err := a.SendGeneratedFile(context.Background(), msg, "report.csv", []byte("id,title\n"), "text/csv"); err == nil {
		t.Fatal("revoked report acknowledged")
	}
	if checks != 2 || uploads != 1 {
		t.Fatalf("authorization checks=%d uploads=%d", checks, uploads)
	}
}

func TestGeneratedFileRejectsUnsafeMetadataBeforeNetwork(t *testing.T) {
	a, msg := durableAdapter(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid artifact reached network") }))
	defer server.Close()
	a.api.base = server.URL
	for _, name := range []string{"../report.html", "C:\\secret.html", "report\n.html", "report\x00.html"} {
		if err := a.SendGeneratedFile(context.Background(), msg, name, []byte("data"), "text/html"); err == nil {
			t.Fatalf("unsafe filename accepted: %q", name)
		}
	}
	if err := a.SendGeneratedFile(context.Background(), msg, "report.html", make([]byte, (2<<20)+1), "text/html"); err == nil {
		t.Fatal("unbounded report accepted")
	}
	if err := a.SendGeneratedFile(context.Background(), msg, "report.exe", []byte("data"), "application/octet-stream"); err == nil {
		t.Fatal("unapproved artifact type accepted")
	}
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return nil, im.ErrScopeDenied
	}
	if err := a.SendGeneratedFile(context.Background(), msg, "report.html", []byte("data"), "text/html"); err == nil {
		t.Fatal("unauthorized upload accepted")
	}
}

func TestGeneratedFileRejectsFailedOrUntrustedUploadResults(t *testing.T) {
	for _, body := range []string{`{"success":false,"data":{"url":"https://cdn.deepminer.com.cn/stale.html"}}`, `{"url":"https://cdn.deepminer.com.cn:8443/file.html"}`, `{"url":"https://cdn.deepminer.com.cn.evil.test/file.html"}`, `{"url":"https://secret@cdn.deepminer.com.cn/file.html"}`} {
		t.Run(body, func(t *testing.T) {
			a, msg := durableAdapter(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/bot/file/upload" {
					t.Error("failed or untrusted artifact delivered")
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			a.api.base = server.URL
			if err := a.SendGeneratedFile(context.Background(), msg, "report.html", []byte("data"), "text/html"); err == nil {
				t.Fatal("bad upload accepted")
			}
		})
	}
}
