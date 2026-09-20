package octo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

const apiBase = "https://im.deepminer.com.cn/api"

type apiClient struct {
	token string
	base  string
	http  *http.Client
}

func newAPI(token string) (*apiClient, error) {
	if !strings.HasPrefix(token, "bf_") || strings.ContainsAny(token, " \r\n\t") || len(token) > 512 {
		return nil, errors.New("invalid Octo Bot credential")
	}
	return &apiClient{token: token, base: apiBase, http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *apiClient) post(ctx context.Context, path string, body any, out any) error {
	return c.request(ctx, http.MethodPost, path, body, out)
}

func (c *apiClient) request(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return errors.New("invalid Octo API request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return errAPIUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiStatusError{Status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, wire.MaxPacket+1))
	if err != nil || len(raw) > wire.MaxPacket {
		return errors.New("invalid Octo API response")
	}
	if len(raw) == 0 {
		if out == nil {
			return nil
		}
		return errors.New("empty Octo API response")
	}
	var envelope map[string]json.RawMessage
	if !json.Valid(raw) {
		return errors.New("invalid Octo API JSON")
	}
	_ = json.Unmarshal(raw, &envelope) // Native member lists are raw arrays.
	if success, ok := envelope["success"]; ok && string(success) != "true" {
		return errors.New("Octo API rejected request")
	}
	if data, ok := envelope["data"]; ok {
		raw = data
	}
	if out != nil {
		if string(raw) == "null" || json.Unmarshal(raw, out) != nil {
			return errors.New("invalid Octo API result")
		}
	}
	return nil
}

type registration struct {
	UID   string `json:"robot_id"`
	Token string `json:"im_token"`
	WS    string `json:"ws_url"`
}

func (c *apiClient) register(ctx context.Context) (registration, error) {
	var result registration
	err := c.post(ctx, "/v1/bot/register", map[string]string{"agent_platform": "weknora", "plugin_version": "0.1.0"}, &result)
	if err == nil && (result.UID == "" || result.Token == "" || result.WS == "") {
		err = errors.New("incomplete Octo registration")
	}
	return result, err
}
