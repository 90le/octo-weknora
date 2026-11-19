package octo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/stretchr/testify/require"
)

func TestGroupSyncRecoversOnlyFreshNativeMentionsWithoutDuplicatingRealtime(t *testing.T) {
	a, realtime := durableAdapter(t)
	ctx := context.Background()
	cutoff := time.Now().Add(-20 * time.Second)
	fresh := cutoff.Add(2 * time.Second).Unix()
	channel := realtime.ChatID
	mention := `{"type":1,"content":"question","mention":{"uids":["bot"]}}`
	quoteOnly := `{"type":1,"content":"for coworker","mention":{"uids":["coworker"]},` +
		`"reply":{"from_uid":"bot","payload":{"type":1,"content":"previous answer"}}}`
	entry := func(id string, seq int, stamp int64, payload string) map[string]any {
		return map[string]any{
			"message_id": json.Number(id), "message_seq": seq, "timestamp": stamp,
			"channel_id": channel, "channel_type": 5, "from_uid": "ordinary-user",
			"payload": base64.StdEncoding.EncodeToString([]byte(payload)),
		}
	}
	deleted := entry("104", 4, fresh, mention)
	deleted["is_deleted"] = 1
	otherScope := entry("105", 5, fresh, mention)
	otherScope["channel_id"] = "other____123"
	realtime.Extra["octo_addressed"] = "true"
	dispatches := 0
	route := func(_ context.Context, _ *im.IncomingMessage) error {
		dispatches++
		return nil
	}
	require.NoError(t, a.accept(ctx, realtime, route))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/bot/messages/sync" {
			t.Errorf("unexpected Octo request: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Start uint32 `json:"start_message_seq"`
			Kind  byte   `json:"channel_type"`
			ID    string `json:"channel_id"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Kind != 5 || body.ID != channel {
			t.Errorf("invalid sync body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Start != 0 && body.Start != 7 {
			t.Errorf("unexpected cursor %d", body.Start)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]any{
				entry("103", 3, fresh, mention),
				entry("101", 1, cutoff.Unix()-1, mention),
				entry("102", 2, fresh, quoteOnly),
				deleted,
				entry(realtime.Extra["octo_message_id"], 6, fresh, mention),
				otherScope,
			},
		})
	}))
	t.Cleanup(server.Close)
	a.api.base = server.URL
	scope := octointegration.Scope{
		GroupID: realtime.Extra["octo_group_id"], SubareaID: realtime.Extra["octo_subarea_id"],
	}
	cursor, err := a.syncGroupQuestions(ctx, scope, 0, cutoff, route)
	require.NoError(t, err)
	require.EqualValues(t, 6, cursor)
	require.Equal(t, 2, dispatches)
	var accepted im.IncomingMessage
	require.NoError(t, json.Unmarshal([]byte(inboxRow(t, a, "octo:bot:103").Input), &accepted))
	require.Equal(t, "true", accepted.Extra["octo_group_sync_recovered"])
	require.Equal(t, "ordinary-user", accepted.UserID)
	var count int64
	require.NoError(t, a.db.Model(&Inbox{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
	_, err = a.syncGroupQuestions(ctx, scope, cursor, cutoff, route)
	require.NoError(t, err)
	require.Equal(t, 2, dispatches)
}
