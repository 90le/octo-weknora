package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
)

func TestSanitizeBody(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		secrets    []string
		safeValues []string
	}{
		{
			name: "browser device credentials",
			in: `{"pairing_link":"wss://example.com/#secret",` +
				`"next_token":"new-secret","deviceToken":"device-secret"}`,
			secrets: []string{"wss://example.com/#secret", "new-secret", "device-secret"},
		},
		{
			name:    "camelCase apiKey",
			in:      `{"modelName":"gpt-5.2","apiKey":"sk-secret-123","provider":"azure_openai"}`,
			secrets: []string{"sk-secret-123"}, safeValues: []string{"gpt-5.2", "azure_openai"},
		},
		{
			name:    "snake_case api_key",
			in:      `{"api_key":"sk-secret-123"}`,
			secrets: []string{"sk-secret-123"},
		},
		{
			name:    "PascalCase APIKey",
			in:      `{"APIKey":"sk-secret-123"}`,
			secrets: []string{"sk-secret-123"},
		},
		{
			name:    "secretKey camelCase",
			in:      `{"secretKey":"abc","accessKeyId":"id"}`,
			secrets: []string{"abc", "id"},
		},
		{
			name:    "refreshToken / accessToken camelCase",
			in:      `{"refreshToken":"refresh-token-value","accessToken":"access-token-value"}`,
			secrets: []string{"refresh-token-value", "access-token-value"},
		},
		{
			name:    "password and token preserved as masked",
			in:      `{"password":"password-value","token":"token-value"}`,
			secrets: []string{"password-value", "token-value"},
		},
		{
			name:    "sandbox terminal handshake ticket in JSON body",
			in:      `{"success":true,"data":{"ticket":"eyJhbGciOiJIUzI1NiJ9.payload.signature","expires_in":120}}`,
			secrets: []string{"eyJhbGciOiJIUzI1NiJ9.payload.signature"}, safeValues: []string{"expires_in"},
		},
		{
			name:    "snake_case new_password and old_password",
			in:      `{"email":"alice@example.com","new_password":"FreshPass9","old_password":"OldPass9"}`,
			secrets: []string{"FreshPass9", "OldPass9"}, safeValues: []string{"alice@example.com"},
		},
		{
			name:    "extra whitespace around colon",
			in:      `{"apiKey"  :   "leak"}`,
			secrets: []string{"leak"},
		},
		{
			name:       "non sensitive fields untouched",
			in:         `{"baseUrl":"https://example.com","modelName":"gpt"}`,
			safeValues: []string{"https://example.com", "gpt"},
		},
		{
			name:    "OAuth authorization response fields",
			in:      `{"authorization_url":"https://idp.example/authorize?state=secret","authorization_attempt":"secret-state"}`,
			secrets: []string{"https://idp.example/authorize?state=secret", "secret-state"},
		},
		{
			name:    "restart recovery preview and confirmation tokens",
			in:      `{"preview_token":"preview-live-token","confirmation_token":"confirm-live-token","publishToken":"publish-live-token"}`,
			secrets: []string{"preview-live-token", "confirm-live-token", "publish-live-token"},
		},
		{
			name:    "other credential shapes use the generic policy",
			in:      `{"hmac_secret":"hmac-live","secret_access_key":"secret-live","request_nonce":"nonce-live","token_limit":123}`,
			secrets: []string{"hmac-live", "secret-live", "nonce-live"}, safeValues: []string{"123"},
		},
		{
			name:    "credentials container is hidden even for unknown vendor fields",
			in:      `{"credentials":{"new_vendor_name":"unknown-vendor-secret"},"name":"source"}`,
			secrets: []string{"unknown-vendor-secret"}, safeValues: []string{"source"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeBody(tc.in)
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("sanitizeBody leaked %q: %s", secret, got)
				}
			}
			for _, value := range tc.safeValues {
				if !strings.Contains(got, value) {
					t.Fatalf("sanitizeBody unexpectedly removed safe value %q: %s", value, got)
				}
			}
			if len(tc.secrets) > 0 && !strings.Contains(got, `"***"`) {
				t.Fatalf("sanitizeBody did not leave a redaction marker: %s", got)
			}
		})
	}
}

func TestSanitizeBodyRedactsSignedTokenInFormBody(t *testing.T) {
	got := sanitizeBody("mode=purge_generated&preview_token=preview-live-token&name=source")
	if strings.Contains(got, "preview-live-token") || !strings.Contains(got, "preview_token=%2A%2A%2A") {
		t.Fatalf("sanitizeBody() did not redact form preview token: %q", got)
	}
}

func TestReadRequestBodyRedactsBeforeTruncationAndPreservesHandlerBody(t *testing.T) {
	// Put the token value across the old logging limit. Before this fix the
	// logger truncated first, producing invalid JSON that skipped redaction and
	// leaked the beginning of the signed confirmation token.
	token := "preview-token-must-not-leak"
	prefix := `{"padding":"`
	separator := `","preview_token":"`
	padding := strings.Repeat("x", maxBodySize-len(prefix)-len(separator)-3)
	body := prefix + padding + separator + token + `"}`
	req := httptest.NewRequest(http.MethodPost, "/datasource/ds/restart-recovery", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	logged := readRequestBody(c)
	if strings.Contains(logged, token) {
		t.Fatalf("readRequestBody leaked preview token across truncation: %q", logged[len(logged)-128:])
	}
	replayed, err := io.ReadAll(c.Request.Body)
	if err != nil || string(replayed) != body {
		t.Fatalf("request body was not preserved for handler: err=%v len=%d", err, len(replayed))
	}
}

func TestLoggerMiddlewareNeverEmitsPreviewToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var captured bytes.Buffer
	logger.SetOutput(&captured)
	t.Cleanup(logger.ConfigureFromEnv)

	r := gin.New()
	r.Use(Logger())
	r.POST("/datasource/:id/restart-recovery", func(c *gin.Context) {
		var payload map[string]string
		_ = c.ShouldBindJSON(&payload)
		c.JSON(http.StatusAccepted, gin.H{"bot_token": "response-bot-token-live", "accepted": true})
	})
	token := "preview-token-live-value"
	credential := "unknown-vendor-credential-live"
	appSecret := "application-secret-live"
	req := httptest.NewRequest(http.MethodPost, "/datasource/ds/restart-recovery", strings.NewReader(`{"preview_token":"`+token+`","credentials":{"vendor":"`+credential+`"},"app_secret":"`+appSecret+`","mode":"recover"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), req)

	logOutput := captured.String()
	for _, secret := range []string{token, credential, appSecret, "response-bot-token-live"} {
		if strings.Contains(logOutput, secret) {
			t.Fatalf("generic request logger leaked %q: %s", secret, logOutput)
		}
	}
	if !strings.Contains(logOutput, `"preview_token":"***"`) {
		t.Fatalf("generic request logger did not write the redacted token marker: %s", logOutput)
	}
	if !strings.Contains(logOutput, `"bot_token":"***"`) {
		t.Fatalf("generic response logger did not redact the response token: %s", logOutput)
	}
}

func TestSanitizeQuery(t *testing.T) {
	got := sanitizeQuery("code=secret-code&state=secret-state&next=%2Fsettings&state=second")
	want := "code=%2A%2A%2A&next=%2Fsettings&state=%2A%2A%2A"
	if got != want {
		t.Fatalf("sanitizeQuery() = %q, want %q", got, want)
	}
}

func TestSanitizeQueryRedactsPreviewTokenByGenericFieldPolicy(t *testing.T) {
	got := sanitizeQuery("preview_token=preview-live-token&next=%2Frecovery")
	if strings.Contains(got, "preview-live-token") || !strings.Contains(got, "preview_token=%2A%2A%2A") {
		t.Fatalf("sanitizeQuery() did not redact preview token: %q", got)
	}
}

func TestSanitizeHTTPBodyFailsClosedForMalformedJSONAndText(t *testing.T) {
	if got := sanitizeHTTPBodyForLog("application/json", []byte(`{"preview_token":"partial`)); strings.Contains(got, "partial") || got != "[invalid JSON body omitted]" {
		t.Fatalf("malformed JSON must fail closed, got %q", got)
	}
	if got := sanitizeHTTPBodyForLog("text/plain", []byte("preview_token=plaintext-secret")); strings.Contains(got, "plaintext-secret") || got != "[body omitted]" {
		t.Fatalf("text body must fail closed, got %q", got)
	}
}

// The sandbox terminal presents its handshake credential as a query parameter
// because a browser WebSocket upgrade cannot carry Authorization. Holding that
// value for its TTL is enough to open a shell in the session's sandbox, so the
// redaction is a security boundary, not cosmetics.
func TestSanitizeQueryRedactsTerminalTicket(t *testing.T) {
	got := sanitizeQuery("ticket=eyJhbGciOiJIUzI1NiJ9.payload.signature&provision=1&cols=120")
	want := "cols=120&provision=1&ticket=%2A%2A%2A"
	if got != want {
		t.Fatalf("sanitizeQuery() = %q, want %q", got, want)
	}
	if strings.Contains(got, "signature") {
		t.Fatalf("sanitizeQuery() leaked the ticket: %q", got)
	}
}
