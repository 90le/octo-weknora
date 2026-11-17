package octointegration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type friendTestTransport func(*http.Request) (*http.Response, error)

func (f friendTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCheckDMFriendDistinguishesDenialFromUnavailable(t *testing.T) {
	const empty = `{"start_message_seq":0,"end_message_seq":0,"pull_mode":1,"messages":[]}`
	for _, test := range []struct {
		name       string
		status     int
		body       string
		friend     bool
		unverified bool
	}{
		{"friends", 200, empty, true, false},
		{"wrapped", 200, `{"success":true,"data":` + empty + `}`, true, false},
		{"non-friend", 400, `{"error":{"code":"err.server.bot_api.not_friend"}}`, false, false},
		{"rate limited", 429, `{}`, false, true},
		{"unauthenticated", 401, `{}`, false, true},
		{"redirect", 307, `{}`, false, true},
		{"wrong shape", 200, `{"messages":[]}`, false, true},
		{"error envelope", 200, `{"success":false,"data":` + empty + `}`, false, true},
		{"unexpected messages", 200, `{"start_message_seq":0,"end_message_seq":0,"messages":[{}]}`, false, true},
		{"oversized result", 200, strings.Repeat(" ", 16*1024) + empty, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: friendTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, officialAPI+"/v1/bot/messages/sync", r.URL.String())
				require.Equal(t, "Bearer bf_example", r.Header.Get("Authorization"))
				require.Empty(t, r.Header.Get("X-Space-ID"))
				var request map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, map[string]any{
					"channel_id": "peer", "channel_type": float64(1), "limit": float64(1), "pull_mode": float64(1),
					"start_message_seq": float64(4294967294), "end_message_seq": float64(4294967295),
				}, request)
				return &http.Response{
					StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)),
					Header: http.Header{"Location": []string{"https://example.invalid/"}},
				}, nil
			})}
			allowed, err := CheckDMFriend(context.Background(), "bf_example", "peer", client)
			require.Equal(t, test.friend, allowed)
			if test.unverified {
				require.ErrorIs(t, err, ErrPlatform)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, calls, "no retries or redirects")
		})
	}
}
