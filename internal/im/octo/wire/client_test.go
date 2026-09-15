package wire

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConnectionCancellationClosesSocketBeforeAuthentication(t *testing.T) {
	connected := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		h, _, _, err := Next(data)
		if err != nil || h != 0x10 {
			return
		}
		close(connected)
		_, _, _ = c.ReadMessage()
		close(closed)
	}))
	defer server.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runConnection(ctx, c, Credentials{UID: "bot", Token: "im-test"}, func(context.Context, *Message) error { t.Error("dispatched before authentication"); return nil })
	}()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("no connect packet")
	}
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not cancel")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("socket leaked")
	}
}

func TestRejectUntrustedSocketOrigins(t *testing.T) {
	for _, address := range []string{"ws://im.deepminer.com.cn/ws", "wss://evil.test/ws", "wss://im.deepminer.com.cn.evil.test/ws", "wss://user@im.deepminer.com.cn/ws", "wss://im.deepminer.com.cn:444/ws"} {
		err := RunConnection(context.Background(), Credentials{UID: "bot", Token: "test", URL: address}, func(context.Context, *Message) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "untrusted") {
			t.Fatalf("accepted %s: %v", address, err)
		}
	}
}
