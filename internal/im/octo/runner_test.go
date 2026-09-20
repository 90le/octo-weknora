package octo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

func TestTransportRecoveryPreservesKickAndAuthenticationStop(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failure   error
		reconnect bool
	}{
		{"server-restart", &wire.DisconnectError{Reason: 15}, true},
		{"route-change", &wire.DisconnectError{Reason: 17}, true},
		{"rate-limit", &wire.DisconnectError{Reason: 22}, true},
		{"network", errors.New("connection closed"), true},
		{"same-bot-kicked", &wire.DisconnectError{Reason: 12}, false},
		{"unknown-server-kick", &wire.DisconnectError{Reason: 0}, false},
		{"authentication", wire.ErrAuthentication, false},
		{"protocol", wire.ErrProtocol, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registered := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				registered++
				_, _ = w.Write([]byte(`{"robot_id":"bot","im_token":"test","ws_url":"wss://im.deepminer.com.cn/ws"}`))
			}))
			defer srv.Close()
			a := &Adapter{uid: "bot", api: &apiClient{http: srv.Client(), base: srv.URL}}
			calls, waits := 0, 0
			connect := func(_ context.Context, _ wire.Credentials, _ func(context.Context, *wire.Message) error, onReady ...func()) error {
				calls++
				onReady[0]()
				if a.RuntimeStatus().State != "online" {
					t.Fatal("not online after auth")
				}
				if calls == 1 {
					return tc.failure
				}
				return wire.ErrAuthentication
			}
			wait := func(context.Context, time.Duration) error {
				waits++
				if a.RuntimeStatus().State != "reconnecting" {
					t.Fatal("retry state missing")
				}
				return nil
			}
			err := a.run(context.Background(), func(context.Context, *im.IncomingMessage) error { return nil }, connect, wait)
			want := 1
			if tc.reconnect {
				want = 2
			}
			if calls != want || registered != want || waits != want-1 || err == nil || a.RuntimeStatus().State != "stopped" {
				t.Fatalf("calls=%d registration=%d waits=%d error=%v status=%+v", calls, registered, waits, err, a.RuntimeStatus())
			}
		})
	}
}

func TestRegistrationRetryAndCredentialFailure(t *testing.T) {
	for _, code := range []int{401, 403, 429, 500, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.WriteHeader(code)
					return
				}
				_, _ = w.Write([]byte(`{"robot_id":"bot","im_token":"test","ws_url":"wss://im.deepminer.com.cn/ws"}`))
			}))
			defer srv.Close()
			a := &Adapter{uid: "bot", api: &apiClient{http: srv.Client(), base: srv.URL}}
			connected := 0
			_ = a.run(context.Background(), func(context.Context, *im.IncomingMessage) error { return nil }, func(context.Context, wire.Credentials, func(context.Context, *wire.Message) error, ...func()) error {
				connected++
				return wire.ErrAuthentication
			}, func(context.Context, time.Duration) error { return nil })
			if code == 401 || code == 403 {
				if calls != 1 || connected != 0 {
					t.Fatal("credential rejection retried")
				}
			} else if calls != 2 || connected != 1 {
				t.Fatalf("transient registration not recovered: %d %d", calls, connected)
			}
		})
	}
}
