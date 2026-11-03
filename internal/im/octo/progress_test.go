package octo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
)

const supportedProgressProfile = `{
	"enabled":true,
	"config":{"card_enabled":true,"reasoning_enabled":true,
		"reasoning_template_ref":{"id":"ai.reasoning-process","version":"0.4.1"}},
	"templating":{"supported":true,"wire":"template-ref/v1","templates":[
		{"id":"ai.reasoning-process","version":"0.4.1","views":[
			{"name":"active","wire_profile":"octo/v2","states":["reasoning","answering"],"submit_actions":[]},
			{"name":"result","wire_profile":"octo/v1","states":["completed","stopped"],"submit_actions":[]},
			{"name":"error","wire_profile":"octo/v2","states":["error"],"submit_actions":[]}
		]}
	]}
}`

func TestProgressProfileFailsClosedForUnsupportedActionsAndVersions(t *testing.T) {
	var profile progressProfile
	if err := json.Unmarshal([]byte(supportedProgressProfile), &profile); err != nil || !profile.supported() {
		t.Fatal("compatible advertised template rejected")
	}
	for _, replacement := range []struct{ old, new string }{
		{`"reasoning_enabled":true`, `"reasoning_enabled":false`},
		{`"submit_actions":[]`, `"submit_actions":["reasoning_stop"]`},
		{`"version":"0.4.1"`, `"version":"9.0.0"`},
	} {
		var p progressProfile
		raw := strings.Replace(supportedProgressProfile, replacement.old, replacement.new, 1)
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		if p.supported() {
			t.Fatalf("unsupported profile accepted: %s", replacement.new)
		}
	}
}

func TestProgressCardUsesFixedStagesAndScopedTarget(t *testing.T) {
	a, msg := durableAdapter(t)
	msg.ChatType = im.ChatTypeDirect
	msg.Extra["octo_channel_type"] = "1"
	scope := &im.ExecutionScope{
		AccountID: "account", ScopeID: "dm:user", Revision: "r", KnowledgeBaseIDs: []string{"kb"},
	}
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return scope, nil
	}
	input, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.Create(&Inbox{
		ChannelID: a.channelID, TenantID: a.tenantID, MessageID: msg.MessageID,
		Input: string(input), Authority: encodeAuthority(scope), State: "processing",
	}).Error; err != nil {
		t.Fatal(err)
	}
	sent := make(chan map[string]json.RawMessage, 1)
	edits := make(chan map[string]json.RawMessage, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/card/profile":
			_, _ = w.Write([]byte(supportedProgressProfile))
		case "/v1/bot/sendMessage":
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent <- body
			_, _ = w.Write([]byte(`{"message_id":"123456"}`))
		case "/v1/bot/message/edit":
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			edits <- body
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	p := a.StartProcessingProgress(context.Background(), msg)
	if p == nil {
		t.Fatal("authorized direct-message progress unavailable")
	}
	select {
	case body := <-sent:
		if string(body["channel_id"]) != `"user"` || strings.Contains(string(body["payload"]), "Original question") {
			t.Fatal("progress target or content is unsafe")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("progress card not sent")
	}
	p.Report("knowledge")
	select {
	case body := <-edits:
		if string(body["state"]) != `"reasoning"` || !strings.Contains(string(body["data"]), "知识库") {
			t.Fatalf("unexpected knowledge progress: %s", body["data"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("progress update missing")
	}
	p.Finish(im.ProcessingCompleted)
	select {
	case body := <-edits:
		var data map[string]any
		if err := json.Unmarshal(body["data"], &data); err != nil || string(body["state"]) != `"completed"` ||
			data["traceExpanded"] != false || data["traceCollapsed"] != true ||
			!strings.Contains(string(body["data"]), "知识库") {
			t.Fatalf("unexpected terminal progress: state=%s data=%s err=%v", body["state"], body["data"], err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("progress completion missing")
	}
	select {
	case <-p.(*progressCard).done:
	case <-time.After(2 * time.Second):
		t.Fatal("progress worker did not finish")
	}
	var saved im.IncomingMessage
	if err := json.Unmarshal([]byte(inboxRow(t, a, msg.MessageID).Input), &saved); err != nil ||
		saved.Extra["octo_progress_message_id"] != "123456" || saved.Extra["octo_progress_closed"] != "true" {
		t.Fatal("progress identity and completion not persisted for recovery")
	}
}

func TestProgressCardIgnoresUnaddressedGroups(t *testing.T) {
	a, msg := durableAdapter(t)
	if a.StartProcessingProgress(context.Background(), msg) != nil {
		t.Fatal("group progress must not turn silent group messages into visible replies")
	}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	a.api.base = server.URL
	msg.Quote = &im.QuotedMessage{IsBotMessage: true}
	progress := a.StartProcessingProgress(context.Background(), msg)
	if progress == nil {
		t.Fatal("a reply to the bot should permit a progress attempt")
	}
	progress.Finish(im.ProcessingFailed)
	select {
	case <-progress.(*progressCard).done:
	case <-time.After(2 * time.Second):
		t.Fatal("progress capability fallback did not finish")
	}
}

func TestProgressCardGroupMentionUsesOriginalGroup(t *testing.T) {
	a, msg := durableAdapter(t)
	msg.Extra["octo_addressed"] = "true"
	scope := &im.ExecutionScope{AccountID: "a", ScopeID: "group", Revision: "r", KnowledgeBaseIDs: []string{"kb"}}
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return scope, nil
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.Create(&Inbox{
		ChannelID: a.channelID, TenantID: a.tenantID, MessageID: msg.MessageID,
		Input: string(raw), Authority: encodeAuthority(scope), State: "processing",
	}).Error; err != nil {
		t.Fatal(err)
	}
	sent := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/card/profile":
			_, _ = w.Write([]byte(supportedProgressProfile))
		case "/v1/bot/sendMessage":
			var body struct {
				Channel string `json:"channel_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent <- body.Channel
			_, _ = w.Write([]byte(`{"message_id":"123456"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	p := a.StartProcessingProgress(context.Background(), msg)
	select {
	case target := <-sent:
		if target != msg.Extra["octo_channel_id"] {
			t.Fatalf("group progress redirected to %q", target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("authorized group progress was not sent")
	}
	p.Finish(im.ProcessingCompleted)
	select {
	case <-p.(*progressCard).done:
	case <-time.After(2 * time.Second):
		t.Fatal("group progress worker did not stop")
	}
}

func TestProgressCardStopsSendingAfterAuthorizationRevoked(t *testing.T) {
	a, msg := durableAdapter(t)
	msg.ChatType = im.ChatTypeDirect
	msg.Extra["octo_channel_type"] = "1"
	scope := &im.ExecutionScope{AccountID: "a", ScopeID: "dm", Revision: "r", KnowledgeBaseIDs: []string{"kb"}}
	var revoked atomic.Bool
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		if revoked.Load() {
			return nil, im.ErrScopeDenied
		}
		return scope, nil
	}
	raw, _ := json.Marshal(msg)
	if err := a.db.Create(&Inbox{
		ChannelID: a.channelID, TenantID: a.tenantID, MessageID: msg.MessageID,
		Input: string(raw), Authority: encodeAuthority(scope), State: "processing",
	}).Error; err != nil {
		t.Fatal(err)
	}
	sent := make(chan struct{}, 1)
	var edits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/card/profile":
			_, _ = w.Write([]byte(supportedProgressProfile))
		case "/v1/bot/sendMessage":
			sent <- struct{}{}
			_, _ = w.Write([]byte(`{"message_id":"123456"}`))
		case "/v1/bot/message/edit":
			edits.Add(1)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	p := a.StartProcessingProgress(context.Background(), msg)
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("progress card not sent")
	}
	revoked.Store(true)
	p.Report("knowledge")
	p.Finish(im.ProcessingCompleted)
	select {
	case <-p.(*progressCard).done:
	case <-time.After(2 * time.Second):
		t.Fatal("progress worker did not stop")
	}
	if edits.Load() != 0 {
		t.Fatal("progress was sent after authorization was revoked")
	}
}

func TestProgressRecoveryFinishesCardWithoutSendingAnotherMessage(t *testing.T) {
	a, msg := durableAdapter(t)
	msg.ChatType = im.ChatTypeDirect
	msg.Extra["octo_channel_type"] = "1"
	msg.Extra["octo_progress_message_id"] = "123456"
	scope := &im.ExecutionScope{AccountID: "a", ScopeID: "dm", Revision: "r", KnowledgeBaseIDs: []string{"kb"}}
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		return scope, nil
	}
	raw, _ := json.Marshal(msg)
	row := Inbox{
		ChannelID: a.channelID, TenantID: a.tenantID, MessageID: msg.MessageID,
		Input: string(raw), Authority: encodeAuthority(scope), State: "delivered", UpdatedAt: time.Now(),
	}
	if err := a.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	var sends, edits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/bot/card/profile":
			_, _ = w.Write([]byte(supportedProgressProfile))
		case "/v1/bot/sendMessage":
			sends.Add(1)
		case "/v1/bot/message/edit":
			edits.Add(1)
			var body struct {
				State string `json:"state"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.State != "completed" {
				t.Errorf("wrong recovered state %q", body.State)
			}
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	a.api.base = server.URL
	a.recoverProgressCards(context.Background())
	a.recoverProgressCards(context.Background())
	if sends.Load() != 0 || edits.Load() != 1 {
		t.Fatalf("recovery sent %d new messages and %d edits", sends.Load(), edits.Load())
	}
}
