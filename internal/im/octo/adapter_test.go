package octo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

func TestNormalizationPreservesScopeAndNativeIdentity(t *testing.T) {
	a, _ := NewAdapter("bf_test", "knowledge_bot")
	if m, err := a.Normalize(&wire.Message{ID: "0", Payload: json.RawMessage(`{"type":1000}`)}); err != nil || m != nil {
		t.Fatal("system event stopped receiver")
	}
	if m, err := a.Normalize(&wire.Message{ID: "123", Sender: "peer_bot", Stream: true}); err != nil || m != nil {
		t.Fatal("stream fragment became question")
	}
	raw := &wire.Message{ID: "2098355867442221056", Sender: "user", Channel: "group____2098355867442221056", ChannelType: 5, Payload: json.RawMessage(`{"type":1,"content":"@小丘 请问","mention":{"uids":["knowledge_bot"]},"reply":{"message_id":2098355867442221057,"from_uid":"knowledge_bot","payload":{"type":1,"content":"之前的问题"}}}`)}
	m, err := a.Normalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.ChatID != raw.Channel || m.Extra["octo_subarea_id"] != "2098355867442221056" || m.Extra["octo_group_id"] != "group" || m.Extra["octo_addressed"] != "true" || m.ThreadID != "" {
		t.Fatalf("scope lost: %+v", m)
	}
	if m.Quote.MessageID != "2098355867442221057" || !m.Quote.IsBotMessage || m.Quote.Content != "之前的问题" {
		t.Fatal("quote lost")
	}
	raw.Payload = json.RawMessage(`{"type":1,"content":"@knowledge_bot"}`)
	m, err = a.Normalize(raw)
	if err != nil || m.Extra["octo_addressed"] != "false" {
		t.Fatal("display text became native mention")
	}
	raw.Sender = "knowledge_bot"
	if m, err = a.Normalize(raw); err != nil || m != nil {
		t.Fatal("self loop")
	}
	raw.Sender = "user"
	raw.ChannelType = 1
	raw.Channel = "knowledge_bot"
	m, err = a.Normalize(raw)
	if err != nil || m.ChatType != im.ChatTypeDirect || m.UserID != "user" {
		t.Fatal("DM sender lost")
	}
}

func TestReplyUsesOriginalTargetQuoteAndUTF16Mention(t *testing.T) {
	var sent map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer bf_test" {
			t.Error("missing credential")
		}
		if json.NewDecoder(r.Body).Decode(&sent) != nil {
			t.Error("invalid request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message_id":2098355867442221058}`))
	}))
	defer server.Close()
	a, _ := NewAdapter("bf_test", "bot")
	a.api.base = server.URL
	m, err := a.Normalize(&wire.Message{ID: "2098355867442221056", Sender: "user", Channel: "group____2098355867442221056", ChannelType: 5, Payload: json.RawMessage(`{"type":1,"content":"原问题"}`)})
	if err != nil {
		t.Fatal(err)
	}
	m.UserName = "丘😀"
	err = a.SendReply(context.Background(), m, &im.ReplyMessage{Content: "回答", IsFinal: true, Extra: map[string]string{"channel_id": "other", "mention_all": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(sent["channel_id"]) != `"group____2098355867442221056"` || string(sent["channel_type"]) != "5" {
		t.Fatal("redirected reply")
	}
	var payload struct {
		Content string `json:"content"`
		Mention struct {
			Entities []wire.Entity `json:"entities"`
			All      int           `json:"all"`
		} `json:"mention"`
		Reply map[string]json.RawMessage `json:"reply"`
	}
	if json.Unmarshal(sent["payload"], &payload) != nil || payload.Content != "@丘😀 回答" || payload.Mention.Entities[0].Length != 4 || payload.Mention.All != 0 {
		t.Fatal("invalid native mention")
	}
	if string(payload.Reply["message_id"]) != `"2098355867442221056"` || !strings.Contains(string(payload.Reply["payload"]), "原问题") {
		t.Fatal("blank quote snapshot")
	}
}

func TestAPIRejectsRedirectAndDoesNotEchoSecrets(t *testing.T) {
	called := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer origin.Close()
	api, _ := newAPI("bf_secret")
	api.base = origin.URL
	err := api.post(context.Background(), "/test", struct{}{}, nil)
	if err == nil || called || strings.Contains(err.Error(), "bf_secret") {
		t.Fatal("unsafe redirect handling")
	}
}

func TestRejectUnsignedCallbackAndUnsafeAttachment(t *testing.T) {
	a, _ := NewAdapter("bf_test", "bot")
	if a.VerifyCallback(nil) == nil {
		t.Fatal("unsigned callback accepted")
	}
	if _, err := a.ParseCallback(nil); err == nil {
		t.Fatal("unsigned payload accepted")
	}
	if a.Run(context.Background(), nil) == nil {
		t.Fatal("ungated runner accepted")
	}
	for _, url := range []string{"http://127.0.0.1/secret", "https://cdn.deepminer.com.cn.evil.test/x", "https://user@cdn.deepminer.com.cn/x"} {
		if _, _, err := a.DownloadFile(context.Background(), &im.IncomingMessage{FileKey: url}); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
}
