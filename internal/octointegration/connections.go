package octointegration

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrEncryption = errors.New("Octo credential encryption unavailable")

type Connection struct {
	TenantID  uint64    `json:"-" gorm:"primaryKey"`
	AccountID string    `json:"account_id" gorm:"primaryKey"`
	Token     string    `json:"-"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Connection) TableName() string { return "octo_connections" }

func (s *Store) Connections(ctx context.Context, tenant uint64) ([]Connection, error) {
	if tenant == 0 {
		return nil, ErrInvalid
	}
	rows := []Connection{}
	err := s.db.WithContext(ctx).Select("account_id, updated_at").Where("tenant_id = ?", tenant).Order("account_id").Find(&rows).Error
	return rows, err
}

func (s *Store) PutConnection(ctx context.Context, tenant uint64, account, token string) error {
	if tenant == 0 || !validID(account) || !strings.HasPrefix(token, "bf_") || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		return ErrInvalid
	}
	key := utils.GetAESKey()
	// Native crypto supports legacy plaintext; new Octo credentials do not.
	if len(key) != 32 {
		return ErrEncryption
	}
	encrypted, err := utils.EncryptAESGCM(token, key)
	if err != nil {
		return ErrEncryption
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		c := Connection{TenantID: tenant, AccountID: account, Token: encrypted, UpdatedAt: time.Now()}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "account_id"}}, DoUpdates: clause.AssignmentColumns([]string{"token", "updated_at"})}).Create(&c).Error; err != nil {
			return err
		}
		if err := tx.Model(&Scope{}).Where("tenant_id = ? AND account_id = ?", tenant, account).Updates(map[string]interface{}{"sync_status": "needs_refresh", "sync_error": ""}).Error; err != nil {
			return err
		}
		return audit(tx, ctx, tenant, account, "octo.connection.updated", map[string]interface{}{"credential_changed": true})
	})
}

func (s *Store) connection(ctx context.Context, tenant uint64, account string) (*Connection, string, error) {
	if tenant == 0 {
		return nil, "", ErrInvalid
	}
	var c Connection
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", tenant, account).First(&c).Error; err != nil {
		return nil, "", err
	}
	if !strings.HasPrefix(c.Token, utils.EncPrefix) {
		return nil, "", ErrEncryption
	}
	token, err := utils.DecryptStoredSecret(c.Token)
	if err != nil || token == "" {
		return nil, "", ErrEncryption
	}
	return &c, token, nil
}

// SyncName retains the last good name on network/access failures. VerifiedAt
// is never advanced on failure, and the status makes stale metadata explicit.
func (s *Store) SyncName(ctx context.Context, tenant uint64, id string, p *platformClient) (*Scope, error) {
	scope, err := s.Get(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	c, token, err := s.connection(ctx, tenant, scope.AccountID)
	if err != nil {
		return nil, err
	}
	metadata, fetchErr := p.scope(ctx, token, *scope)
	now := time.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Connection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND account_id = ?", tenant, scope.AccountID).First(&current).Error; err != nil {
			return err
		}
		if current.Token != c.Token {
			return ErrPlatform
		} // discard an in-flight old-credential response
		patch := map[string]interface{}{"checked_at": now, "sync_status": "error", "sync_error": "platform_verification_failed"}
		if fetchErr == nil {
			patch["display_name"] = metadata.Name
			patch["name_source"] = "octo"
			patch["verified_at"] = now
			patch["sync_status"] = "verified"
			patch["sync_error"] = ""
		}
		if err := tx.Model(&Scope{}).Where("tenant_id = ? AND id = ?", tenant, id).Updates(patch).Error; err != nil {
			return err
		}
		return audit(tx, ctx, tenant, id, "octo.scope.synchronized", map[string]interface{}{"verified": fetchErr == nil})
	})
	if err != nil {
		return nil, err
	}
	if fetchErr != nil {
		return nil, fetchErr
	}
	return s.Get(ctx, tenant, id)
}

func (s *Store) CreateVerified(ctx context.Context, scope Scope, p *platformClient) (*Scope, error) {
	if scope.TenantID == 0 || !validID(scope.AccountID) {
		return nil, ErrInvalid
	}
	c, token, err := s.connection(ctx, scope.TenantID, scope.AccountID)
	if err != nil {
		return nil, err
	}
	metadata, err := p.scope(ctx, token, scope)
	if err != nil {
		return nil, err
	}
	scope.DisplayName = metadata.Name
	var result *Scope
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Connection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND account_id = ?", scope.TenantID, scope.AccountID).First(&current).Error; err != nil {
			return err
		}
		if current.Token != c.Token {
			return ErrPlatform
		}
		created, err := NewStore(tx).Create(ctx, scope)
		if err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(&Scope{}).Where("tenant_id = ? AND id = ?", scope.TenantID, created.ID).Updates(map[string]interface{}{"name_source": "octo", "sync_status": "verified", "verified_at": now, "checked_at": now}).Error; err != nil {
			return err
		}
		result, err = NewStore(tx).Get(ctx, scope.TenantID, created.ID)
		return err
	})
	return result, err
}
