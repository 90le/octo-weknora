package octo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/octobusiness"
)

const (
	testCOSURL = "https://im-data-1255521909.cos.ap-beijing.myqcloud.com/reports/file?signature=storage-secret"
	testCDNURL = "https://cdn.deepminer.com.cn/reports/file.html"
)

type fileRoundTripper func(*http.Request) (*http.Response, error)

func (f fileRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Transport-only tests inject an explicitly permissive journal. Crash and
// replay semantics are tested separately against the durable SQLite journal.
type transportTestFileJournal struct{}

func (transportTestFileJournal) start(context.Context, fileTurn, string) error { return nil }
func (transportTestFileJournal) recovery(context.Context, fileTurn) (bool, bool, error) {
	return true, false, nil
}

func (transportTestFileJournal) reserve(context.Context, fileTurn, string, fileDeliveryEntry) (bool, error) {
	return false, nil
}

func (transportTestFileJournal) markUploading(context.Context, fileTurn, string, string) error {
	return nil
}

func (transportTestFileJournal) markSending(context.Context, fileTurn, string, string) error {
	return nil
}

func (transportTestFileJournal) finish(context.Context, fileTurn, string, string, string, string, string) error {
	return nil
}

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
	a.fileJournal = transportTestFileJournal{}
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
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer bf_test" ||
				r.URL.Query().Get("filename") != name ||
				r.URL.Query().Get("fileSize") != fmt.Sprint(len(data)) ||
				r.URL.Query().Get("contentType") != "text/html" {
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

func TestGeneratedFileRejectsChangedReportAuthorityAtEveryStage(t *testing.T) {
	for _, revokeAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(revokeAt), func(t *testing.T) {
			a, msg := generatedFileFixture(t)
			p := octobusiness.Principal{
				TenantID:         1,
				AccountID:        "account",
				ChannelID:        "channel",
				ScopeID:          "scope",
				UserID:           "user",
				KnowledgeBaseIDs: []string{"one", "two"},
			}
			fresh := p
			p.Validate = func(context.Context) (octobusiness.Principal, error) { return fresh, nil }
			ctx := octobusiness.WithReportAuthority(octobusiness.WithPrincipal(context.Background(), p), p)
			if revokeAt == 1 {
				fresh.KnowledgeBaseIDs = []string{"one"}
			}
			presigns, uploads := 0, 0
			a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
				switch r.Method {
				case http.MethodGet:
					presigns++
					if revokeAt == 2 {
						fresh.KnowledgeBaseIDs = []string{"one"}
					}
					return fileResponse(200, filePresignFixture(4)), nil
				case http.MethodPut:
					uploads++
					if revokeAt == 3 {
						fresh.KnowledgeBaseIDs = []string{"one"}
					}
					return fileResponse(200, nil), nil
				default:
					t.Error("report with partially revoked knowledge disclosed")
					return fileResponse(500, nil), nil
				}
			})
			err := a.SendGeneratedFile(ctx, msg, "report.csv", []byte("data"), "text/csv")
			if !errors.Is(err, octobusiness.ErrDenied) || presigns != boolCount(revokeAt > 1) ||
				uploads != boolCount(revokeAt > 2) {
				t.Fatalf("report scope changed: err=%v presigns=%d uploads=%d", err, presigns, uploads)
			}
		})
	}
}

func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestFileDeliveryToolUsesOriginalConversationAndStopsAtEveryRevocation(t *testing.T) {
	for _, revokeAt := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprint("revokeAt=", revokeAt), func(t *testing.T) {
			a, msg := generatedFileFixture(t)
			p := octobusiness.Principal{
				TenantID: 1, AccountID: "account", ChannelID: "channel", ScopeID: "scope",
				UserID: "user", MessageID: msg.MessageID, MessageText: "生成 Markdown 文件",
				KnowledgeBaseIDs: []string{"one"}, AllowGeneratedFiles: true,
			}
			fresh := p
			p.Validate = func(context.Context) (octobusiness.Principal, error) { return fresh, nil }
			ctx := octobusiness.WithPrincipal(context.Background(), p)
			ctx = octobusiness.WithFileDeliverySink(ctx, func(
				ctx context.Context, file *octobusiness.ReportArtifact,
			) error {
				if revokeAt == 1 {
					fresh.AllowGeneratedFiles = false
				}
				return a.SendGeneratedFile(ctx, msg, file.Name, []byte(file.Content), file.ContentType)
			}, func(context.Context) error { return nil })
			var presigns, uploads, sends int
			a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
				switch r.Method {
				case http.MethodGet:
					presigns++
					if revokeAt == 2 {
						fresh.AllowGeneratedFiles = false
					}
					return fileResponse(200, filePresignFixture(4)), nil
				case http.MethodPut:
					uploads++
					if revokeAt == 3 {
						fresh.AllowGeneratedFiles = false
					}
					return fileResponse(200, nil), nil
				case http.MethodPost:
					var body struct {
						Channel string `json:"channel_id"`
						Payload struct {
							Type int    `json:"type"`
							Name string `json:"name"`
						} `json:"payload"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body.Channel != msg.ChatID || body.Payload.Type != 8 ||
						body.Payload.Name != "knowledge-summary.md" {
						t.Error("Markdown export was redirected or did not use a file message")
					}
					sends++
					return fileResponse(200, map[string]string{"message_id": "2098355867442221058"}), nil
				default:
					t.Error("unexpected file request")
					return fileResponse(500, nil), nil
				}
			})
			result, err := octobusiness.NewFileDeliveryTool().
				Execute(ctx, json.RawMessage(`{"name":"knowledge-summary.md","content":"data"}`))
			if err != nil || result.Success != (revokeAt == 0) || sends != boolCount(revokeAt == 0) ||
				presigns != boolCount(
					revokeAt == 0 || revokeAt > 1,
				) || uploads != boolCount(revokeAt == 0 || revokeAt > 2) {
				t.Fatalf(
					"unexpected delivery: result=%+v presigns=%d uploads=%d sends=%d err=%v",
					result,
					presigns,
					uploads,
					sends,
					err,
				)
			}
		})
	}
}

func TestBinaryFileDeliveryAuthorityStopsAtEveryRevocation(t *testing.T) {
	for _, revokeAt := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprint("revokeAt=", revokeAt), func(t *testing.T) {
			a, msg := generatedFileFixture(t)
			p := octobusiness.Principal{
				TenantID: 1, AccountID: "account", ChannelID: "channel", ScopeID: "scope",
				UserID: "user", MessageID: msg.MessageID, KnowledgeBaseIDs: []string{"one"},
				AllowGeneratedFiles: true, AllowFileDelivery: true,
			}
			fresh := p
			p.Validate = func(context.Context) (octobusiness.Principal, error) { return fresh, nil }
			ctx := octobusiness.WithFileDeliverySink(octobusiness.WithPrincipal(context.Background(), p),
				func(context.Context, *octobusiness.ReportArtifact) error { return nil },
				func(context.Context) error { return nil })
			ctx, err := octobusiness.WithCurrentFileDeliveryAuthority(ctx, true)
			if err != nil {
				t.Fatal(err)
			}
			if revokeAt == 1 {
				fresh.AllowFileDelivery = false
			}
			presigns, uploads, sends := 0, 0, 0
			a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
				switch r.Method {
				case http.MethodGet:
					presigns++
					if revokeAt == 2 {
						fresh.AllowFileDelivery = false
					}
					return fileResponse(200, filePresignFixture(4)), nil
				case http.MethodPut:
					uploads++
					if revokeAt == 3 {
						fresh.AllowFileDelivery = false
					}
					return fileResponse(200, nil), nil
				case http.MethodPost:
					sends++
					return fileResponse(200, map[string]string{"message_id": "1"}), nil
				default:
					t.Error("unexpected request")
					return fileResponse(500, nil), nil
				}
			})
			err = a.SendGeneratedFile(ctx, msg, "报表.xlsx", []byte{'P', 'K', 0, 0xff}, "")
			if (revokeAt == 0 && err != nil) || (revokeAt != 0 && !errors.Is(err, octobusiness.ErrDenied)) ||
				presigns != boolCount(
					revokeAt == 0 || revokeAt > 1,
				) || uploads != boolCount(revokeAt == 0 || revokeAt > 2) || sends != boolCount(revokeAt == 0) {
				t.Fatalf("binary file authority: err=%v presigns=%d uploads=%d sends=%d", err, presigns, uploads, sends)
			}
		})
	}
}

func TestGeneratedFileRejectsUnsafeMetadataBeforeNetwork(t *testing.T) {
	a, msg := generatedFileFixture(t)
	a.api.http.Transport = fileRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Error("invalid artifact reached network")
		return fileResponse(500, nil), nil
	})
	for _, name := range []string{
		"../report.html", "dir/report.html", "C:\\secret.html",
		"report:alternate.html", "report\n.html", "report\x00.html", "report\x01.html", "report\x7f.html",
		"bad\".html", " report.html", "report.html ", "report.", ".", "..", "", string([]byte{
			0xff, '.', 't',
			'x', 't',
		}), strings.Repeat("名", 256) + ".html",
	} {
		if err := a.SendGeneratedFile(context.Background(), msg, name, []byte("data"), "text/html"); err == nil {
			t.Fatalf("unsafe filename accepted: %q", name)
		}
	}
	if err := a.SendGeneratedFile(context.Background(), msg, "report.html", nil, "text/html"); err == nil {
		t.Fatal("empty artifact accepted")
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
	}{{"local HTTP", "uploadUrl", "http://127.0.0.1/private"}, {
		"local HTTPS", "uploadUrl",
		"https://127.0.0.1/private",
	}, {
		"provider lookalike", "uploadUrl",
		"https://bucket-123.cos.ap-beijing.myqcloud.com.evil.test/file",
	}, {
		"unknown provider", "uploadUrl",
		"https://other.myqcloud.com/file",
	}, {
		"userinfo", "uploadUrl",
		"https://user@im-data-1255521909.cos.ap-beijing.myqcloud.com/file",
	}, {
		"storage port", "uploadUrl",
		"https://im-data-1255521909.cos.ap-beijing.myqcloud.com:8443/file",
	}, {
		"CDN port", "downloadUrl",
		"https://cdn.deepminer.com.cn:8443/file",
	}, {
		"wrong CDN", "downloadUrl",
		"https://cdn.deepminer.com.cn.evil.test/file",
	}, {"wrong size", "maxFileSize", 5}, {
		"wrong method",
		"method", "POST",
	}, {"empty content type", "contentType", ""}, {
		"injected disposition",
		"contentDisposition", "file\r\nAuthorization: private",
	}, {
		"control in content type", "contentType",
		"application/\x01octet-stream",
	}, {"control in disposition", "contentDisposition", "attachment;\x7f"}}
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

func TestGeneratedFileBinaryFormatsAndMIMEHints(t *testing.T) {
	tests := []struct {
		name, filename, supplied, hint, signedType string
		data                                       []byte
	}{
		{
			"word",
			"合同.docx",
			"",
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			"application/octet-stream",
			[]byte{'P', 'K', 3, 4, 0, 255, 1},
		},
		{
			"excel generic MIME",
			"报表.xlsx",
			"application/octet-stream",
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"application/octet-stream",
			[]byte{'P', 'K', 3, 4, 128, 0, 2},
		},
		{
			"pdf invalid MIME",
			"报告.pdf",
			"bad MIME\r\nInjected: value",
			"application/pdf",
			"application/pdf",
			[]byte{'%', 'P', 'D', 'F', 0, 254, 3},
		},
		{"mismatched MIME", "报告.pdf", "audio/mpeg", "audio/mpeg", "application/pdf", []byte{'%', 'P', 'D', 'F', 0, 1}},
		{
			"server configured extension",
			"导出 🐙.octocustom",
			"application/x-example",
			"application/x-example",
			"application/x-example",
			[]byte{0, 1, 2, 0xff},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, msg := generatedFileFixture(t)
			presigns, uploads, sends := 0, 0, 0
			a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.URL.Host == "im.deepminer.com.cn" && r.URL.Path == "/api/v1/bot/upload/presigned":
					presigns++
					if r.URL.Query().Get("filename") != tc.filename ||
						r.URL.Query().Get("fileSize") != fmt.Sprint(len(tc.data)) ||
						r.URL.Query().Get("contentType") != tc.hint {
						t.Errorf("unexpected presign query: %v", r.URL.Query())
					}
					p := filePresignFixture(len(tc.data))
					p["contentType"] = tc.signedType
					return fileResponse(200, p), nil
				case strings.HasSuffix(r.URL.Host, ".myqcloud.com"):
					uploads++
					actual, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(actual, tc.data) || r.ContentLength != int64(len(tc.data)) {
						t.Errorf("binary upload changed: %v %x", err, actual)
					}
					if r.Header.Get("Content-Type") != tc.signedType {
						t.Error("PUT did not use the server's signed Content-Type")
					}
					return fileResponse(200, nil), nil
				case r.URL.Host == "im.deepminer.com.cn" && r.URL.Path == "/api/v1/bot/sendMessage":
					sends++
					var body struct {
						Channel string `json:"channel_id"`
						Payload struct {
							Name string `json:"name"`
							Size int    `json:"size"`
						} `json:"payload"`
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || body.Channel != msg.ChatID ||
						body.Payload.Name != tc.filename ||
						body.Payload.Size != len(tc.data) {
						t.Errorf("file metadata or destination changed: %+v", body)
					}
					return fileResponse(200, map[string]string{"message_id": "1"}), nil
				default:
					t.Errorf("unexpected request: %s", r.URL.Host+r.URL.Path)
					return fileResponse(500, nil), nil
				}
			})
			if err := a.SendGeneratedFile(context.Background(), msg, tc.filename, tc.data, tc.supplied); err != nil ||
				presigns != 1 ||
				uploads != 1 ||
				sends != 1 {
				t.Fatalf("delivery failed: err=%v presigns=%d uploads=%d sends=%d", err, presigns, uploads, sends)
			}
		})
	}
}

func TestGeneratedFileLocalSizeBoundary(t *testing.T) {
	a, msg := generatedFileFixture(t)
	// One allocation also exercises a file above the old 2 MiB limit and the
	// exact 20 MiB boundary without retaining copies of multiple large files.
	data := make([]byte, maxGeneratedFileBytes+1)
	data[0], data[maxGeneratedFileBytes-1] = 0, 0xff
	presigns, uploads, sends := 0, 0, 0
	a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			presigns++
			size, err := strconv.Atoi(r.URL.Query().Get("fileSize"))
			if err != nil {
				t.Fatal(err)
			}
			return fileResponse(200, filePresignFixture(size)), nil
		case http.MethodPut:
			uploads++
			n, err := io.Copy(io.Discard, r.Body)
			if err != nil || n != r.ContentLength {
				t.Fatalf("upload size: %d, want %d, err=%v", n, r.ContentLength, err)
			}
			return fileResponse(200, nil), nil
		case http.MethodPost:
			sends++
			return fileResponse(200, map[string]string{"message_id": "1"}), nil
		default:
			t.Fatal("unexpected request")
			return nil, nil
		}
	})
	for _, size := range []int{(2 << 20) + 1, maxGeneratedFileBytes} {
		if err := a.SendGeneratedFile(context.Background(), msg, "large.pdf", data[:size], ""); err != nil {
			t.Fatalf("size %d rejected: %v", size, err)
		}
	}
	if err := a.SendGeneratedFile(context.Background(), msg, "large.pdf", data, ""); err == nil {
		t.Fatal("file exceeding the local 20 MiB limit was accepted")
	}
	if presigns != 2 || uploads != 2 || sends != 2 {
		t.Fatalf("unexpected network at size boundary: presigns=%d uploads=%d sends=%d", presigns, uploads, sends)
	}
}

func TestGeneratedFileHonorsServerRejections(t *testing.T) {
	// The server's dynamic allowlist and size limit are authoritative. The fake
	// rejects an executable, an unsupported extension, and an oversized file.
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"report.exe", http.StatusBadRequest},
		{"report.not-supported", http.StatusBadRequest},
		{"report.rejected", http.StatusOK}, // application-level rejection envelope
		{"report.docx", http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, msg := generatedFileFixture(t)
			requests := 0
			a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.Method != http.MethodGet || r.URL.Host != "im.deepminer.com.cn" ||
					r.URL.Query().Get("filename") != tc.name {
					t.Error("server-rejected file reached storage or send")
				}
				return fileResponse(tc.status, map[string]any{"success": false}), nil
			})
			err := a.SendGeneratedFile(
				context.Background(),
				msg,
				tc.name,
				[]byte{0, 1, 0xff},
				"application/octet-stream",
			)
			var failure *fileFailure
			if !errors.As(err, &failure) || failure.code != "presign_failed" || requests != 1 {
				t.Fatalf("server rejection was not honored: err=%v requests=%d", err, requests)
			}
		})
	}
}

func TestGeneratedFileRetryIdentityIncludesContentsAndConversation(t *testing.T) {
	a, msg := generatedFileFixture(t)
	originalMessageID := msg.MessageID
	var ids []string
	var names []string
	a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			size, err := strconv.Atoi(r.URL.Query().Get("fileSize"))
			if err != nil {
				t.Fatal(err)
			}
			return fileResponse(200, filePresignFixture(size)), nil
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			return fileResponse(200, nil), nil
		case http.MethodPost:
			var body struct {
				Client  string `json:"client_msg_no"`
				Channel string `json:"channel_id"`
				Payload struct {
					Name string `json:"name"`
				} `json:"payload"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Channel != msg.ChatID || body.Client == "" {
				t.Fatal("invalid file send")
			}
			ids, names = append(ids, body.Client), append(names, body.Payload.Name)
			if len(ids) == 1 {
				return fileResponse(http.StatusServiceUnavailable, nil), nil
			}
			return fileResponse(200, map[string]string{"message_id": "1"}), nil
		default:
			t.Fatal("unexpected request")
			return nil, nil
		}
	})
	first := []byte{0, 1, 2, 0xff}
	second := []byte{0, 1, 3, 0xff} // same length, different binary content
	if err := a.SendGeneratedFile(context.Background(), msg, "same.pdf", first, "application/pdf"); err == nil {
		t.Fatal("simulated send failure was ignored")
	}
	for _, file := range []struct {
		name, mime string
		data       []byte
	}{
		{"same.pdf", "application/octet-stream", first},
		{"same.pdf", "application/pdf", second},
		{"other.pdf", "application/pdf", first},
	} {
		if err := a.SendGeneratedFile(context.Background(), msg, file.name, file.data, file.mime); err != nil {
			t.Fatal(err)
		}
	}
	msg.MessageID = "another-message"
	if err := a.SendGeneratedFile(context.Background(), msg, "same.pdf", first, "application/pdf"); err != nil {
		t.Fatal(err)
	}
	msg.MessageID = originalMessageID
	msg.ChatID = "another-group____2098355867442221056"
	msg.Extra["octo_channel_id"] = msg.ChatID
	if err := a.SendGeneratedFile(context.Background(), msg, "same.pdf", first, "application/pdf"); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 6 || ids[0] != ids[1] || ids[0] == ids[2] || ids[0] == ids[3] || ids[0] == ids[4] ||
		ids[0] == ids[5] ||
		names[2] != "same.pdf" ||
		names[3] != "other.pdf" {
		t.Fatalf("incorrect retry or multi-file identity: ids=%v names=%v", ids, names)
	}
}
