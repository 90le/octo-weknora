package octo

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
)

func TestGeneratedFileWithResultSVGFallback(t *testing.T) {
	a, msg, ctx, _ := journalFixture(t)
	data := []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><text>你好</text></svg>")
	gets, puts, sends := 0, 0, 0
	a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			gets++
			if r.URL.Query().Get("filename") == "drawing.svg" {
				return fileResponse(400, map[string]any{
					"error": map[string]string{"code": "err.server.bot_api.file_type_unsupported"},
				}), nil
			}
			if r.URL.Query().Get("filename") != "drawing.svg.zip" {
				t.Fatal("wrong archive name")
			}
			size, _ := strconv.Atoi(r.URL.Query().Get("fileSize"))
			return fileResponse(200, filePresignFixture(size)), nil
		case http.MethodPut:
			puts++
			body, _ := io.ReadAll(r.Body)
			z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
			if err != nil || len(z.File) != 1 || z.File[0].Name != "drawing.svg" {
				t.Fatal("invalid zip", err)
			}
			member, err := z.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(member)
			_ = member.Close()
			if err != nil || !bytes.Equal(data, content) {
				t.Fatal("changed bytes")
			}
			return fileResponse(200, nil), nil
		case http.MethodPost:
			sends++
			var p struct {
				Payload struct {
					Name string `json:"name"`
				} `json:"payload"`
			}
			if json.NewDecoder(r.Body).Decode(&p) != nil || p.Payload.Name != "drawing.svg.zip" {
				t.Fatal("wrong sent name")
			}
			return fileResponse(200, map[string]string{"message_id": "123"}), nil
		}
		return nil, errors.New("unexpected request")
	})
	for i := 0; i < 2; i++ {
		name, err := a.SendGeneratedFileWithResult(ctx, msg, "drawing.svg", data, "")
		if err != nil || name != "drawing.svg.zip" {
			t.Fatalf("name=%q err=%v", name, err)
		}
	}
	if gets != 3 || puts != 1 || sends != 1 {
		t.Fatalf("gets=%d puts=%d sends=%d", gets, puts, sends)
	}
}

func TestGeneratedFileFallbackFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, data, code string
		status           int
		legacy           bool
	}{
		{"evil.exe", "MZ", "err.server.bot_api.file_type_unsupported", 400, false},
		{"unknown.bin", "data", "err.server.bot_api.file_type_unsupported", 400, false},
		{"drawing.svg", "MZ", "err.server.bot_api.file_type_unsupported", 400, false},
		{"drawing.svg", "<svg/><svg/>", "err.server.bot_api.file_type_unsupported", 400, false},
		{"drawing.svg", "<svg/>", "other", 400, false},
		{"drawing.svg", "<svg/>", "err.server.bot_api.file_type_unsupported", 500, false},
		{"drawing.svg", "<svg/>", "err.server.bot_api.file_type_unsupported", 400, true},
	} {
		t.Run(tc.name+tc.code+strconv.Itoa(tc.status)+strconv.FormatBool(tc.legacy), func(t *testing.T) {
			a, msg := generatedFileFixture(t)
			calls := 0
			a.api.http.Transport = fileRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return fileResponse(tc.status, map[string]any{"error": map[string]string{"code": tc.code}}), nil
			})
			var err error
			if tc.legacy {
				err = a.SendGeneratedFile(context.Background(), msg, tc.name, []byte(tc.data), "")
			} else {
				_, err = a.SendGeneratedFileWithResult(context.Background(), msg, tc.name, []byte(tc.data), "")
			}
			if err == nil || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestGeneratedFileFallbackUploadUnknownDoesNotRetry(t *testing.T) {
	a, msg, ctx, _ := journalFixture(t)
	gets, puts := 0, 0
	a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			gets++
			if r.URL.Query().Get("filename") == "drawing.svg" {
				return fileResponse(400, map[string]any{
					"error": map[string]string{"code": "err.server.bot_api.file_type_unsupported"},
				}), nil
			}
			size, _ := strconv.Atoi(r.URL.Query().Get("fileSize"))
			return fileResponse(200, filePresignFixture(size)), nil
		case http.MethodPut:
			puts++
			return nil, errors.New("upload unavailable")
		}
		t.Fatal("unexpected send")
		return nil, nil
	})
	for i := 0; i < 2; i++ {
		name, err := a.SendGeneratedFileWithResult(ctx, msg, "drawing.svg", []byte("<svg/>"), "")
		var failure *fileFailure
		if name != "" || !errors.As(err, &failure) || failure.DeliveryOutcome() != "unknown" {
			t.Fatalf("name=%q err=%v", name, err)
		}
	}
	if puts != 1 || gets != 3 {
		t.Fatalf("gets=%d puts=%d", gets, puts)
	}
}

func TestGeneratedFileFallbackRechecksAuthorization(t *testing.T) {
	a, msg, ctx, _ := journalFixture(t)
	calls := 0
	a.api.http.Transport = fileRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
			return nil, im.ErrScopeDenied
		}
		return fileResponse(400, map[string]any{
			"error": map[string]string{"code": "err.server.bot_api.file_type_unsupported"},
		}), nil
	})
	name, err := a.SendGeneratedFileWithResult(ctx, msg, "drawing.svg", []byte("<svg/>"), "")
	if name != "" || err == nil || calls != 1 {
		t.Fatalf("name=%q err=%v calls=%d", name, err, calls)
	}
}

func TestGeneratedFileWithResultReturnsSentName(t *testing.T) {
	a, msg := generatedFileFixture(t)
	a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			return fileResponse(200, filePresignFixture(4)), nil
		case http.MethodPut:
			return fileResponse(200, nil), nil
		case http.MethodPost:
			return fileResponse(200, map[string]string{"message_id": "123"}), nil
		}
		return nil, errors.New("unexpected request")
	})
	name, err := a.SendGeneratedFileWithResult(context.Background(), msg, "report.pdf", []byte("data"), "")
	if err != nil || name != "report.pdf" {
		t.Fatalf("name=%q err=%v", name, err)
	}
}
