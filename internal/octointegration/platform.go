package octointegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const officialAPI = "https://im.deepminer.com.cn/api"

var ErrPlatform = errors.New("Octo platform verification failed")

type platformClient struct {
	client  *http.Client
	baseURL string
}

func newPlatformClient() *platformClient {
	return &platformClient{baseURL: officialAPI, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

// platformJSON never includes tokens or upstream response bodies in errors.
func (p *platformClient) get(ctx context.Context, token, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return ErrPlatform
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	r, err := p.client.Do(req)
	if err != nil {
		return ErrPlatform
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrPlatform, r.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024+1))
	if err != nil || len(b) > 2*1024*1024 {
		return ErrPlatform
	}
	var envelope struct {
		Data    json.RawMessage `json:"data"`
		Success *bool           `json:"success"`
	}
	if json.Unmarshal(b, &envelope) == nil {
		if envelope.Success != nil && !*envelope.Success {
			return ErrPlatform
		}
		if len(envelope.Data) > 0 {
			if string(envelope.Data) == "null" {
				return ErrPlatform
			}
			b = envelope.Data
		}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return ErrPlatform
	}
	return nil
}

type platformScope struct {
	GroupID   string `json:"group_no"`
	SubareaID string `json:"short_id"`
	Name      string `json:"name"`
	Status    *int   `json:"status"`
}

func (p *platformClient) scope(ctx context.Context, token string, s Scope) (*platformScope, error) {
	if !validID(s.GroupID) || s.GroupID == "." || s.GroupID == ".." || strings.ContainsAny(s.GroupID, "/\\?#%") || (s.SubareaID != "" && (!validID(s.SubareaID) || s.SubareaID == "." || s.SubareaID == ".." || strings.ContainsAny(s.SubareaID, "/\\?#%"))) {
		return nil, ErrInvalid
	}
	path := "/v1/bot/groups/" + url.PathEscape(s.GroupID)
	if s.SubareaID != "" {
		path += "/threads/" + url.PathEscape(s.SubareaID)
	}
	var v platformScope
	if err := p.get(ctx, token, path, &v); err != nil {
		return nil, err
	}
	if v.GroupID != s.GroupID || v.SubareaID != s.SubareaID || v.Status == nil || *v.Status != 1 || strings.TrimSpace(v.Name) == "" || len(v.Name) > 256 {
		return nil, ErrPlatform
	}
	return &v, nil
}

type platformMember struct {
	UID      string `json:"uid"`
	Role     *int   `json:"role"`
	Robot    *int   `json:"robot"`
	BotAdmin *int   `json:"bot_admin"`
}

type MemberRole struct {
	UID            string `json:"uid"`
	Kind           string `json:"kind"`
	Role           string `json:"role"`
	CanManageGroup bool   `json:"can_manage_group"`
	Reason         string `json:"reason"`
}

// This is a diagnostic result for workspace administrators. It does not grant
// WeKnora asset permissions and is not trusted evidence from a chat client.
func resolveMember(m platformMember) MemberRole {
	r := MemberRole{UID: m.UID, Kind: "unknown", Role: "unknown", Reason: "incomplete_platform_identity"}
	if m.Robot == nil {
		return r
	}
	switch *m.Robot {
	case 0:
		r.Kind = "human"
		if m.Role == nil {
			return r
		}
		switch *m.Role {
		case 0:
			r.Role = "member"
		case 1:
			r.Role = "owner"
			r.CanManageGroup = true
		case 2:
			r.Role = "admin"
			r.CanManageGroup = true
		default:
			return r
		}
		r.Reason = "native_group_role"
	case 1:
		r.Kind = "bot"
		r.Role = "member"
		r.Reason = "bot_admin_not_proven"
		if m.BotAdmin != nil && *m.BotAdmin == 1 {
			r.Role = "admin"
			r.CanManageGroup = true
			r.Reason = "native_bot_admin"
		}
	}
	return r
}

func (p *platformClient) member(ctx context.Context, token string, s Scope, uid string) (MemberRole, error) {
	if !validID(uid) {
		return MemberRole{}, ErrInvalid
	}
	// Verify this scope is still accessible before inspecting its parent role.
	if _, err := p.scope(ctx, token, s); err != nil {
		return MemberRole{}, err
	}
	var rows []platformMember
	if err := p.get(ctx, token, "/v1/bot/groups/"+url.PathEscape(s.GroupID)+"/members", &rows); err != nil {
		return MemberRole{}, err
	}
	var found *platformMember
	for i := range rows {
		if rows[i].UID == uid {
			if found != nil {
				return MemberRole{}, ErrPlatform
			}
			found = &rows[i]
		}
	}
	if found == nil {
		return MemberRole{UID: uid, Kind: "unknown", Role: "unknown", Reason: "member_not_found"}, nil
	}
	return resolveMember(*found), nil
}
