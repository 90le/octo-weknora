package im

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrWebOwnerForbidden = errors.New("web channel ownership forbidden")

// WebOwnerExists tests an explicit, active grant. Callers must supply the
// authenticated Web user and tenant, never identities from request parameters.
// A disabled user, inactive membership, or deleted channel cannot use a grant.
func WebOwnerExists(ctx context.Context, db *gorm.DB, tenantID uint64, channelID, userID string) (bool, error) {
	if tenantID == 0 || strings.TrimSpace(channelID) == "" || strings.TrimSpace(userID) == "" {
		return false, nil
	}
	var count int64
	err := db.WithContext(ctx).Table("im_channel_web_owners AS o").
		Joins("JOIN im_channels c ON c.id = o.im_channel_id AND c.tenant_id = o.tenant_id AND c.platform = ? AND c.deleted_at IS NULL", "octo").
		Joins("JOIN tenant_members m ON m.tenant_id = o.tenant_id AND m.user_id = o.web_user_id AND m.status = ? AND m.deleted_at IS NULL", "active").
		Joins("JOIN users u ON u.id = o.web_user_id AND u.is_active = ? AND u.deleted_at IS NULL", true).
		Where("o.tenant_id = ? AND o.im_channel_id = ? AND o.web_user_id = ? AND o.deleted_at IS NULL", tenantID, channelID, userID).Count(&count).Error
	return count > 0, err
}

type webOwner struct {
	ID          string `gorm:"primaryKey"`
	TenantID    uint64
	IMChannelID string
	WebUserID   string
	CreatedAt   time.Time
	DeletedAt   gorm.DeletedAt
}

func (webOwner) TableName() string { return "im_channel_web_owners" }

// SQLite preserves duplicate JSON object keys, whereas Go and PostgreSQL use
// the last value. Reject duplicate identity keys so those views cannot diverge.
func validWebOwnerIdentity(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return false
		}
		name, ok := key.(string)
		if !ok {
			return false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		if name == "account_id" || name == "bot_uid" {
			if seen[name] {
				return false
			}
			seen[name] = true
			var identity string
			if json.Unmarshal(value, &identity) != nil || strings.TrimSpace(identity) == "" || strings.IndexFunc(identity, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
				return false
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return false
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return false
	}
	if !seen["account_id"] || !seen["bot_uid"] {
		return false
	}
	channel := IMChannel{Platform: "octo", Mode: "websocket", SessionMode: "user", Credentials: data}
	return channel.validateSessionMode() == nil
}

func webOwnerCaller(ctx context.Context) (types.Caller, error) {
	p, ok := ctx.Value(types.PrincipalContextKey).(types.Principal)
	caller := types.CallerFromContext(ctx)
	_, apiKey := types.TenantAPIKeyScopeFromContext(ctx)
	if apiKey || !ok || p.Type != types.PrincipalWebUser || p.ID == "" || p.ID != caller.UserID || caller.TenantID == 0 {
		return types.Caller{}, ErrWebOwnerForbidden
	}
	return caller, nil
}

// WebOwnerStatus returns only the current authenticated user's binding.
func (s *Service) WebOwnerStatus(ctx context.Context, channelID string) (bool, error) {
	caller, err := webOwnerCaller(ctx)
	if err != nil || strings.TrimSpace(channelID) == "" {
		return false, ErrWebOwnerForbidden
	}
	var count int64
	err = s.db.WithContext(ctx).Table("im_channels AS c").
		Joins("JOIN tenant_members m ON m.tenant_id = c.tenant_id AND m.user_id = ? AND m.status = ? AND m.deleted_at IS NULL", caller.UserID, "active").
		Joins("JOIN users u ON u.id = m.user_id AND u.is_active = ? AND u.deleted_at IS NULL", true).
		Where("c.id = ? AND c.tenant_id = ? AND c.platform = ? AND c.deleted_at IS NULL", channelID, caller.TenantID, "octo").Count(&count).Error
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, ErrWebOwnerForbidden
	}
	return WebOwnerExists(ctx, s.db, caller.TenantID, channelID, caller.UserID)
}

// SetWebOwner binds or unbinds the authenticated user, and records every
// effective change in the same transaction. It never accepts a target user.
func (s *Service) SetWebOwner(ctx context.Context, channelID string, bind bool, requestPath, requestMethod string) error {
	caller, err := webOwnerCaller(ctx)
	if err != nil || strings.TrimSpace(channelID) == "" {
		return ErrWebOwnerForbidden
	}
	userID := caller.UserID
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var member types.TenantMember
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND user_id = ? AND status = ? AND role IN ?", caller.TenantID, userID, "active", []string{"owner", "admin"}).First(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrWebOwnerForbidden
		}
		if err != nil {
			return err
		}
		var user types.User
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND is_active = ?", userID, true).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrWebOwnerForbidden
		}
		if err != nil {
			return err
		}
		// Read credentials as raw text so malformed SQLite JSON fails as a
		// forbidden identity rather than failing the model's JSON scanner.
		var channel struct {
			ID          string
			Credentials string
		}
		err = tx.Table("im_channels").Select("id, credentials").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND platform = ? AND deleted_at IS NULL", channelID, caller.TenantID, "octo").Take(&channel).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrWebOwnerForbidden
		}
		if err != nil {
			return err
		}
		if bind {
			if !validWebOwnerIdentity([]byte(channel.Credentials)) {
				return ErrWebOwnerForbidden
			}
		}
		var result *gorm.DB
		action := types.AuditAction("im_channel.web_owner_unbound")
		if bind {
			action = types.AuditAction("im_channel.web_owner_bound")
			result = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&webOwner{ID: uuid.NewString(), TenantID: caller.TenantID, IMChannelID: channelID, WebUserID: userID})
		} else {
			result = tx.Where("tenant_id = ? AND im_channel_id = ? AND web_user_id = ?", caller.TenantID, channelID, userID).Delete(&webOwner{})
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return tx.Create(&types.AuditLog{TenantID: caller.TenantID, ActorUserID: userID, ActorRole: string(member.Role), Action: action, TargetType: "im_channel", TargetID: channelID, TargetUserID: userID, RequestPath: requestPath, RequestMethod: requestMethod, Outcome: types.AuditOutcomeSuccess, Details: types.JSON("{}")}).Error
	})
}
