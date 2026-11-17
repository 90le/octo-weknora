package octointegration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

var dmFriendHTTP = newPlatformClient().client

// CheckDMFriend uses Octo's own private-message friend gate. The caller must
// first verify which Bot and creator this token belongs to and handle the
// creator locally: Octo's friend gate exempts the creator too.
func CheckDMFriend(ctx context.Context, token, uid string, transport *http.Client) (bool, error) {
	if !validID(uid) || !strings.HasPrefix(token, "bf_") || len(token) <= 3 ||
		len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		return false, ErrPlatform
	}
	// The bot-token API has no dedicated friend-status route. Pull up in an
	// unused range, with no OBO or Space context and no read receipt. Refuse
	// any unexpected response that suggests the range was not honored.
	body, err := json.Marshal(struct {
		ChannelID   string `json:"channel_id"`
		ChannelType int    `json:"channel_type"`
		Start       uint32 `json:"start_message_seq"`
		End         uint32 `json:"end_message_seq"`
		Limit       int    `json:"limit"`
		PullMode    int    `json:"pull_mode"`
	}{uid, 1, math.MaxUint32 - 1, math.MaxUint32, 1, 1})
	if err != nil {
		return false, ErrPlatform
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	endpoint := officialAPI + "/v1/bot/messages/sync"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return false, ErrPlatform
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := *dmFriendHTTP
	if transport != nil {
		// Reuse the runtime transport without copying cookies or redirect rules.
		client.Transport = transport.Transport
	}
	res, err := client.Do(req)
	if err != nil {
		return false, ErrPlatform
	}
	defer res.Body.Close()
	const maxResponse = 16 * 1024
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return false, ErrPlatform
	}
	return dmFriendDecision(res.StatusCode, raw)
}

func dmFriendDecision(status int, raw []byte) (bool, error) {
	var envelope struct {
		Data    json.RawMessage `json:"data"`
		Success *bool           `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false, ErrPlatform
	}
	if status == http.StatusBadRequest && envelope.Error.Code == "err.server.bot_api.not_friend" {
		return false, nil
	}
	if status != http.StatusOK || envelope.Error.Code != "" || (envelope.Success != nil && !*envelope.Success) {
		return false, ErrPlatform
	}
	if len(envelope.Data) != 0 {
		raw = envelope.Data
	}
	var synced struct {
		Start    *uint32         `json:"start_message_seq"`
		End      *uint32         `json:"end_message_seq"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &synced); err != nil {
		return false, ErrPlatform
	}
	if synced.Start == nil || synced.End == nil || len(synced.Messages) == 0 {
		return false, ErrPlatform
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(synced.Messages, &messages); err != nil || len(messages) != 0 {
		return false, ErrPlatform
	}
	return true, nil
}
