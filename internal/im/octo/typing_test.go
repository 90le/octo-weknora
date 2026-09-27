package octo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
)

func typingMessage(kind, channel string) *im.IncomingMessage {
	return &im.IncomingMessage{
		Platform: Platform,
		UserID:   "user",
		Extra: map[string]string{
			"octo_message_id":   "123",
			"octo_channel_id":   channel,
			"octo_channel_type": kind,
		},
	}
}

func TestProcessingTypingUsesNativeScopedTarget(t *testing.T) {
	for _, tc := range []struct {
		name, kind, channel, want string
	}{
		{"dm", "1", "bot", "user"},
		{"group", "2", "group", "group"},
		{"subarea", "5", "group____123", "group____123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan struct {
				Target string `json:"channel_id"`
				Kind   int    `json:"channel_type"`
			}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/bot/typing" || r.Header.Get("Authorization") != "Bearer bf_test" {
					t.Errorf("unexpected typing request: %s", r.URL.Path)
				}
				var body struct {
					Target string `json:"channel_id"`
					Kind   int    `json:"channel_type"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode typing request: %v", err)
				}
				requests <- body
				_, _ = w.Write([]byte(`{"success":true}`))
			}))
			defer server.Close()
			a, _ := NewAdapter("bf_test", "bot")
			a.api.base = server.URL
			stop := a.StartProcessing(context.Background(), typingMessage(tc.kind, tc.channel))
			defer stop()
			select {
			case got := <-requests:
				if got.Target != tc.want || got.Kind != int(tc.kind[0]-'0') {
					t.Fatalf("typing target = %+v, want %s/%s", got, tc.want, tc.kind)
				}
			case <-time.After(time.Second):
				t.Fatal("no native typing request")
			}
		})
	}
}

func TestProcessingTypingBacksOffTransientFailureAndStops(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	a, _ := NewAdapter("bf_test", "bot")
	a.api.base = server.URL
	stop := a.startProcessingWithInterval(context.Background(), typingMessage("2", "group"), 10*time.Millisecond)
	deadline := time.After(time.Second)
	for calls.Load() < 2 {
		select {
		case <-deadline:
			stop()
			t.Fatal("typing was not refreshed")
		case <-time.After(5 * time.Millisecond):
		}
	}
	stop()
	stop() // stopping a completed QA run is idempotent
	final := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != final {
		t.Fatal("typing continued after processing stopped")
	}
}

func TestProcessingTypingStopsAfterTerminalAPIResponse(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			first := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				select {
				case first <- struct{}{}:
				default:
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			a, _ := NewAdapter("bf_test", "bot")
			a.api.base = server.URL
			stop := a.startProcessingWithInterval(context.Background(), typingMessage("2", "group"), 10*time.Millisecond)
			select {
			case <-first:
			case <-time.After(time.Second):
				stop()
				t.Fatal("no first typing attempt")
			}
			time.Sleep(50 * time.Millisecond)
			stop()
			if got := calls.Load(); got != 1 {
				t.Fatalf("terminal HTTP %d was retried %d times", status, got)
			}
		})
	}
}

func TestProcessingTypingSkipsInvalidOrCancelledMessages(t *testing.T) {
	a, _ := NewAdapter("bf_test", "bot")
	for _, msg := range []*im.IncomingMessage{
		nil,
		typingMessage("257", "group"),
		typingMessage("5", "group____bad"),
		typingMessage("2", "group____123"),
	} {
		if target, _, ok := a.typingTarget(msg); ok || target != "" {
			t.Fatalf("invalid message acquired a typing target: %+v", msg)
		}
		// Invalid input must not attempt network I/O.
		a.StartProcessing(context.Background(), msg)()
	}
	self := typingMessage("2", "group")
	self.UserID = "bot"
	if _, _, ok := a.typingTarget(self); ok {
		t.Fatal("bot self-message acquired a typing target")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.StartProcessing(ctx, typingMessage("2", "group"))()
}
