package octo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/octobusiness"
)

const testCOSURL = "https://im-data-1255521909.cos.ap-beijing.myqcloud.com/reports/file?signature=storage-secret"
const testCDNURL = "https://cdn.deepminer.com.cn/reports/file.html"

type fileRoundTripper func(*http.Request) (*http.Response, error)

func (f fileRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fileResponse(status int, body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}
func filePresignFixture(size int) map[string]any {
	return map[string]any{"method": "PUT", "uploadUrl": testCOSURL, "downloadUrl": testCDNURL, "contentType": "application/octet-stream", "contentDisposition": "attachment; filename*=UTF-8''report%20generated.html", "maxFileSize": size, "expiresIn": 600, "expiredTime": 9999999999, "key": "reports/file"}
}
func generatedFileFixture(t *testing.T) (*Adapter, *im.IncomingMessage) {
	t.Helper()
	a, err := NewAdapter("bf_test", "bot")
	if err != nil {
		t.Fatal(err)
	}
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

func TestGeneratedFilePresignedUploadUsesOriginalTargetAndStableIdentity(t *testing.T) {
	a, msg := generatedFileFixture(t)
	data := []byte("<h1>Report</h1>")
	name := "report generated.html"
	jar, _ := cookiejar.New(nil)
	storageURL, _ := url.Parse(testCOSURL)
	jar.SetCookies(storageURL, []*http.Cookie{{Name: "api-cookie", Value: "must-not-leak"}})
	a.api.http.Jar = jar
	var clients []string
	uploads := 0
	presigns := 0
	a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "im.deepminer.com.cn" && r.URL.Path == "/api/v1/bot/upload/presigned":
			presigns++
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer bf_test" || r.URL.Query().Get("filename") != name || r.URL.Query().Get("fileSize") != fmt.Sprint(len(data)) {
				t.Error("invalid authenticated presign request")
			}
			return fileResponse(200, map[string]any{"data": filePresignFixture(len(data))}), nil
		case r.URL.Host == storageURL.Host:
			uploads++
			actual, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if r.Method != http.MethodPut || string(actual) != string(data) || r.ContentLength != int64(len(data)) {
				t.Error("storage method/body/signed content length changed")
			}
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
				t.Fatal("Bot credential or cookie leaked to storage")
			}
			if r.Header.Get("Content-Type") != "application/octet-stream" || r.Header.Get("Content-Disposition") != filePresignFixture(len(data))["contentDisposition"] {
				t.Error("signed headers not echoed verbatim")
			}
			return fileResponse(200, nil), nil
		case r.URL.Host == "im.deepminer.com.cn" && r.URL.Path == "/api/v1/bot/sendMessage":
			if r.Header.Get("Authorization") != "Bearer bf_test" {
				t.Error("missing Bot credential on official send API")
			}
			var body struct {
				Channel string `json:"channel_id"`
				Kind    int    `json:"channel_type"`
				Client  string `json:"client_msg_no"`
				Payload struct {
					Type  int                        `json:"type"`
					Name  string                     `json:"name"`
					URL   string                     `json:"url"`
					Reply map[string]json.RawMessage `json:"reply"`
				} `json:"payload"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid file message")
			}
			clients = append(clients, body.Client)
			if body.Channel != msg.Extra["octo_channel_id"] || body.Kind != 5 || body.Payload.Type != 8 || body.Payload.Name != name || body.Payload.URL != testCDNURL {
				t.Fatalf("artifact redirected or altered: %+v", body)
			}
			if string(body.Payload.Reply["message_id"]) != `"2098355867442221056"` || string(body.Payload.Reply["from_uid"]) != `"user"` || !strings.Contains(string(body.Payload.Reply["payload"]), "Original question") {
				t.Fatal("original quote absent or blank")
			}
			return fileResponse(200, map[string]any{"message_id": "2098355867442221058"}), nil
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Host+r.URL.Path)
			return fileResponse(500, nil), nil
		}
	})
	for i := 0; i < 2; i++ {
		if err := a.SendGeneratedFile(context.Background(), msg, name, data, "text/html"); err != nil {
			t.Fatal(err)
		}
	}
	if presigns != 2 || uploads != 2 || len(clients) != 2 || clients[0] == "" || clients[0] != clients[1] {
		t.Fatal("presign flow or retry idempotency changed")
	}
}

func TestGeneratedFileRechecksAccessBeforeAndAfterStorage(t *testing.T) {
	for _, denyAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(denyAt), func(t *testing.T) {
			a, msg := generatedFileFixture(t)
			checks, presigns, uploads, sends := 0, 0, 0, 0
			a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
				checks++
				if checks >= denyAt {
					return nil, im.ErrScopeDenied
				}
				return &im.ExecutionScope{KnowledgeBaseIDs: []string{"kb"}}, nil
			}
			a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodGet:
					presigns++
					return fileResponse(200, filePresignFixture(4)), nil
				case r.Method == http.MethodPut:
					uploads++
					return fileResponse(200, nil), nil
				default:
					sends++
					return fileResponse(200, map[string]string{"message_id": "1"}), nil
				}
			})
			err := a.SendGeneratedFile(context.Background(), msg, "report.csv", []byte("data"), "text/csv")
			if !errors.Is(err, im.ErrScopeDenied) || sends != 0 {
				t.Fatalf("revoked artifact sent or permission error lost: %v sends=%d", err, sends)
			}
			if (denyAt == 1 && (presigns != 0 || uploads != 0)) || (denyAt == 2 && (presigns != 1 || uploads != 0)) || (denyAt == 3 && (presigns != 1 || uploads != 1)) {
				t.Fatalf("unexpected network after revocation: presigns=%d uploads=%d", presigns, uploads)
			}
		})
	}
}

func TestGeneratedFileRejectsChangedReportAuthorityAfterUpload(t *testing.T) {
	a, msg := generatedFileFixture(t)
	p := octobusiness.Principal{TenantID: 1, AccountID: "account", ChannelID: "channel", ScopeID: "scope", UserID: "user", KnowledgeBaseIDs: []string{"one", "two"}}
	fresh := p
	p.Validate = func(context.Context) (octobusiness.Principal, error) { return fresh, nil }
	ctx := octobusiness.WithReportAuthority(octobusiness.WithPrincipal(context.Background(), p), p)
	a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return fileResponse(200, filePresignFixture(4)), nil
		}
		if r.Method == http.MethodPut {
			fresh.KnowledgeBaseIDs = []string{"one"}
			return fileResponse(200, nil), nil
		}
		t.Error("report with partially revoked knowledge disclosed")
		return fileResponse(500, nil), nil
	})
	if err := a.SendGeneratedFile(ctx, msg, "report.csv", []byte("data"), "text/csv"); !errors.Is(err, octobusiness.ErrDenied) {
		t.Fatalf("report scope changed: %v", err)
	}
}

func TestGeneratedFileRejectsUnsafeMetadataBeforeNetwork(t *testing.T) {
	a, msg := generatedFileFixture(t)
	a.api.http.Transport = fileRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Error("invalid artifact reached network")
		return fileResponse(500, nil), nil
	})
	for _, name := range []string{"../report.html", "C:\\secret.html", "report\n.html", "report\x00.html", ".", ""} {
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
	msg.Extra["octo_channel_type"] = "3"
	if err := a.SendGeneratedFile(context.Background(), msg, "report.html", []byte("data"), "text/html"); err == nil {
		t.Fatal("invalid recipient accepted")
	}
}

func TestGeneratedFileRejectsUntrustedPresignsBeforeStorage(t *testing.T) {
	cases := []struct {
		name, field string
		value       any
	}{{"local HTTP", "uploadUrl", "http://127.0.0.1/private"}, {"local HTTPS", "uploadUrl", "https://127.0.0.1/private"}, {"provider lookalike", "uploadUrl", "https://bucket-123.cos.ap-beijing.myqcloud.com.evil.test/file"}, {"unknown provider", "uploadUrl", "https://other.myqcloud.com/file"}, {"userinfo", "uploadUrl", "https://user@im-data-1255521909.cos.ap-beijing.myqcloud.com/file"}, {"storage port", "uploadUrl", "https://im-data-1255521909.cos.ap-beijing.myqcloud.com:8443/file"}, {"CDN port", "downloadUrl", "https://cdn.deepminer.com.cn:8443/file"}, {"wrong CDN", "downloadUrl", "https://cdn.deepminer.com.cn.evil.test/file"}, {"wrong size", "maxFileSize", 5}, {"wrong method", "method", "POST"}, {"empty content type", "contentType", ""}, {"injected disposition", "contentDisposition", "file\r\nAuthorization: private"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, msg := generatedFileFixture(t)
			p := filePresignFixture(4)
			p[tc.field] = tc.value
			a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Error("invalid presign reached storage or send")
				}
				return fileResponse(200, p), nil
			})
			err := a.SendGeneratedFile(context.Background(), msg, "report.html", []byte("data"), "text/html")
			if err == nil || strings.Contains(err.Error(), "storage-secret") || strings.Contains(err.Error(), "private") {
				t.Fatalf("untrusted presign accepted or leaked: %v", err)
			}
		})
	}
}

func TestGeneratedFileStorageRedirectAndErrorsAreSafe(t *testing.T) {
	for _, mode := range []string{"redirect", "network", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			a, msg := generatedFileFixture(t)
			uploads := 0
			a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					return fileResponse(200, filePresignFixture(4)), nil
				}
				if r.Method != http.MethodPut {
					t.Error("failed file delivery attempted")
					return fileResponse(500, nil), nil
				}
				uploads++
				switch mode {
				case "redirect":
					response := fileResponse(302, nil)
					response.Header.Set("Location", "https://evil.test/secret")
					return response, nil
				case "network":
					return nil, fmt.Errorf("upload signed URL %s", r.URL)
				default:
					return fileResponse(403, map[string]string{"private": "storage-secret"}), nil
				}
			})
			err := a.SendGeneratedFile(context.Background(), msg, "report.csv", []byte("data"), "text/csv")
			if err == nil || uploads != 1 || strings.Contains(err.Error(), "storage-secret") || strings.Contains(err.Error(), "bf_test") || strings.Contains(err.Error(), "https:") {
				t.Fatalf("unsafe upload error: %v uploads=%d", err, uploads)
			}
		})
	}
}

func TestScheduledFileDoesNotInventOriginalQuote(t *testing.T) {
	a, _ := generatedFileFixture(t)
	msg := &im.IncomingMessage{UserID: "owner", MessageID: "schedule-run", Extra: map[string]string{"octo_channel_type": "1", "octo_channel_id": "owner"}}
	a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			return fileResponse(200, filePresignFixture(4)), nil
		case http.MethodPut:
			return fileResponse(200, nil), nil
		default:
			var sent struct {
				Channel string                     `json:"channel_id"`
				Kind    int                        `json:"channel_type"`
				Payload map[string]json.RawMessage `json:"payload"`
			}
			if json.NewDecoder(r.Body).Decode(&sent) != nil {
				t.Fatal("invalid send")
			}
			if sent.Channel != "owner" || sent.Kind != 1 {
				t.Fatal("scheduled private recipient changed")
			}
			if _, ok := sent.Payload["reply"]; ok {
				t.Fatal("scheduled artifact invented a quote")
			}
			return fileResponse(200, map[string]string{"message_id": "2098355867442221058"}), nil
		}
	})
	if err := a.SendGeneratedFile(context.Background(), msg, "report.csv", []byte("data"), "text/csv"); err != nil {
		t.Fatal(err)
	}
}
