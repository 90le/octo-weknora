package octointegration

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
)

var ErrIdentityUnverified = errors.New("Octo Bot identity requires explicit verification")

func (c *Connection) trustedIdentity() *ConnectionIdentity {
	var identity ConnectionIdentity
	if c == nil || json.Unmarshal(c.VerifiedIdentity, &identity) != nil || !validID(identity.BotUID) {
		return nil
	}
	return &identity
}

// The caller just authenticated this UID using this exact credential snapshot.
// Do not advance UpdatedAt: recording a display identity is not a token change.
func (s *Store) RecordVerifiedIdentity(ctx context.Context, tenant uint64, account, ciphertext string, identity ConnectionIdentity) error {
	if tenant == 0 || !validID(account) || !validID(identity.BotUID) || !strings.HasPrefix(ciphertext, utils.EncPrefix) {
		return ErrInvalid
	}
	data, _ := json.Marshal(identity)
	r := s.db.WithContext(ctx).Model(&Connection{}).Where("tenant_id = ? AND account_id = ? AND token = ?", tenant, account, ciphertext).UpdateColumn("verified_identity", types.JSON(data))
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrPlatform
	}
	return nil
}

func (s *Store) knownIdentity(ctx context.Context, tenant uint64, token string) (*ConnectionIdentity, error) {
	if tenant == 0 {
		return nil, ErrInvalid
	}
	var rows []Connection
	if err := s.db.WithContext(ctx).Where("tenant_id = ?", tenant).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		identity := row.trustedIdentity()
		if identity == nil {
			continue
		}
		known, err := utils.DecryptStoredSecret(row.Token)
		if err != nil {
			return nil, ErrEncryption
		}
		if known == token {
			return identity, nil
		}
	}
	return nil, nil
}

// Official GET /v1/bot/user/info requires a UID and returns any user's profile,
// so it refreshes a *previously verified* identity, never proves token ownership.
// In contrast POST /register calls UpdateIMToken and can kick an active socket.
func (p *platformClient) readIdentity(ctx context.Context, token string, verified *ConnectionIdentity) (*ConnectionIdentity, error) {
	if verified == nil || !validID(verified.BotUID) {
		return nil, ErrIdentityUnverified
	}
	var profile struct {
		UID  string `json:"uid"`
		Name string `json:"name"`
	}
	if err := p.get(ctx, token, "/v1/bot/user/info?uid="+url.QueryEscape(verified.BotUID), &profile); err != nil {
		return nil, err
	}
	if profile.UID != verified.BotUID || len(profile.Name) > 256 {
		return nil, ErrPlatform
	}
	return &ConnectionIdentity{BotUID: profile.UID, Name: strings.TrimSpace(profile.Name), OwnerUID: verified.OwnerUID}, nil
}
