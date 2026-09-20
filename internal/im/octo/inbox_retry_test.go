package octo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/stretchr/testify/require"
)

func TestInboxRetryExhaustionIsTerminalWithoutRestart(t *testing.T) {
	a, msg := durableAdapter(t)
	ctx := context.Background()
	require.NoError(t, a.accept(ctx, msg, func(context.Context, *im.IncomingMessage) error { return nil }))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	a.api.base = server.URL
	for attempt := 1; attempt <= 3; attempt++ {
		require.Error(t, a.SendReply(ctx, msg, &im.ReplyMessage{Content: "Stored answer", IsFinal: true}))
		row := inboxRow(t, a, msg.MessageID)
		require.Equal(t, attempt, row.Attempts)
		if attempt < 3 {
			require.Equal(t, "reply_pending", row.State)
		} else {
			require.Equal(t, "failed", row.State)
			require.Equal(t, "delivery_retry_exhausted", row.ErrorCode)
		}
	}
}
func TestInboxExhaustionSweepIsScopedAndKeepsDeliveredRows(t *testing.T) {
	a, msg := durableAdapter(t)
	ctx := context.Background()
	for _, row := range []Inbox{
		{ChannelID: a.channelID, TenantID: a.tenantID, MessageID: msg.MessageID, State: "reply_pending", Attempts: 3},
		{ChannelID: a.channelID, TenantID: a.tenantID, MessageID: "delivered", State: "delivered", Attempts: 3},
		{ChannelID: "other-channel", TenantID: a.tenantID, MessageID: "foreign-channel", State: "reply_pending", Attempts: 3},
		{ChannelID: a.channelID, TenantID: 2, MessageID: "foreign-tenant", State: "reply_pending", Attempts: 3},
	} {
		require.NoError(t, a.db.Create(&row).Error)
	}
	a.markExhaustedReplies(ctx)
	require.Equal(t, "failed", inboxRow(t, a, msg.MessageID).State)
	a.recordReplyFailure(ctx, "delivered", "late_failure")
	require.Equal(t, "delivered", inboxRow(t, a, "delivered").State)
	var remaining int64
	require.NoError(t, a.db.Model(&Inbox{}).Where("state = ?", "reply_pending").Count(&remaining).Error)
	require.EqualValues(t, 2, remaining)
}
