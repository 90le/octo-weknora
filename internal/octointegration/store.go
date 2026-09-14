// Package octointegration owns Octo usage scopes, not knowledge assets or RBAC.
package octointegration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalid = errors.New("invalid Octo scope")

type Scope struct {
	ID          string `json:"id" gorm:"primaryKey"`
	TenantID    uint64 `json:"-"`
	AccountID   string `json:"account_id"`
	GroupID     string `json:"group_id"`
	SubareaID   string `json:"subarea_id"`
	DisplayName string `json:"display_name"`
	// Names entered by an administrator are not claimed to be platform-verified.
	NameSource    string    `json:"name_source"`
	InheritParent bool      `json:"inherit_parent"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (Scope) TableName() string { return "octo_scopes" }

// Binding grants query selection only. It cannot confer KB maintenance rights.
type Binding struct {
	TenantID        uint64    `json:"-" gorm:"primaryKey"`
	ScopeID         string    `json:"scope_id" gorm:"primaryKey"`
	KnowledgeBaseID string    `json:"knowledge_base_id" gorm:"primaryKey"`
	CreatedAt       time.Time `json:"created_at"`
}

func (Binding) TableName() string { return "octo_scope_bindings" }

type EffectiveBinding struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	FromScopeID     string `json:"from_scope_id"`
	Inherited       bool   `json:"inherited"`
}

type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func validID(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n\t")
}

func validateScope(s Scope) error {
	if s.TenantID == 0 || !validID(s.AccountID) || !validID(s.GroupID) ||
		(s.SubareaID != "" && !validID(s.SubareaID)) ||
		len(strings.TrimSpace(s.DisplayName)) == 0 || len(s.DisplayName) > 256 ||
		(s.SubareaID == "" && s.InheritParent) {
		return ErrInvalid
	}
	return nil
}

func (s *Store) Create(ctx context.Context, scope Scope) (*Scope, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	scope.ID = uuid.NewString()
	scope.NameSource = "configured"
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if scope.SubareaID != "" {
			var parent Scope
			if err := tx.Where("tenant_id = ? AND account_id = ? AND group_id = ? AND subarea_id = ''", scope.TenantID, scope.AccountID, scope.GroupID).First(&parent).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&scope).Error; err != nil {
			return err
		}
		return audit(tx, ctx, scope.TenantID, scope.ID, "octo.scope.created", map[string]interface{}{"account_id": scope.AccountID, "group_id": scope.GroupID, "subarea_id": scope.SubareaID})
	})
	return &scope, err
}

func (s *Store) Get(ctx context.Context, tenant uint64, id string) (*Scope, error) {
	if tenant == 0 {
		return nil, ErrInvalid
	}
	var scope Scope
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenant, id).First(&scope).Error
	return &scope, err
}

func (s *Store) List(ctx context.Context, tenant uint64, offset int) ([]Scope, error) {
	if tenant == 0 || offset < 0 {
		return nil, ErrInvalid
	}
	rows := []Scope{}
	err := s.db.WithContext(ctx).Where("tenant_id = ?", tenant).Order("account_id, group_id, subarea_id, id").Limit(100).Offset(offset).Find(&rows).Error
	return rows, err
}

func (s *Store) Update(ctx context.Context, tenant uint64, id, name string, inherit bool) error {
	scope, err := s.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	scope.DisplayName, scope.InheritParent = name, inherit
	if err := validateScope(*scope); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Scope{}).Where("tenant_id = ? AND id = ?", tenant, id).
			Updates(map[string]interface{}{"display_name": name, "name_source": "configured", "inherit_parent": inherit}).Error; err != nil {
			return err
		}
		return audit(tx, ctx, tenant, id, "octo.scope.updated", map[string]interface{}{"display_name": name, "inherit_parent": inherit})
	})
}

// SetBinding never deletes a KB. The caller must already pass native KB write
// authorization. The database query additionally disallows cross-tenant assets.
func (s *Store) SetBinding(ctx context.Context, tenant uint64, scopeID, kbID string, enabled bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := NewStore(tx).Get(ctx, tenant, scopeID); err != nil {
			return err
		}
		if !enabled {
			if err := tx.Where("tenant_id = ? AND scope_id = ? AND knowledge_base_id = ?", tenant, scopeID, kbID).Delete(&Binding{}).Error; err != nil {
				return err
			}
			return audit(tx, ctx, tenant, scopeID, "octo.binding.removed", map[string]interface{}{"knowledge_base_id": kbID})
		}
		var count int64
		if err := tx.Table("knowledge_bases").Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", kbID, tenant).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return gorm.ErrRecordNotFound
		}
		b := Binding{TenantID: tenant, ScopeID: scopeID, KnowledgeBaseID: kbID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&b).Error; err != nil {
			return err
		}
		return audit(tx, ctx, tenant, scopeID, "octo.binding.added", map[string]interface{}{"knowledge_base_id": kbID})
	})
}

// Audit and mutation commit together in the existing native audit table.
func audit(tx *gorm.DB, ctx context.Context, tenant uint64, scope string, action types.AuditAction, details map[string]interface{}) error {
	actor, _ := types.UserIDFromContext(ctx)
	data, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return tx.Create(&types.AuditLog{TenantID: tenant, ActorUserID: actor, ActorRole: string(types.CallerFromContext(ctx).Role), Action: action, ScopeType: "octo_scope", ScopeID: scope, Details: types.JSON(data), CreatedAt: time.Now()}).Error
}

// Effective is an administrative preview, not a public retrieval authorization.
// Runtime access must separately authenticate the Octo caller and its scope.
func (s *Store) Effective(ctx context.Context, tenant uint64, id string) ([]EffectiveBinding, error) {
	result := []EffectiveBinding{}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := NewStore(tx).Get(ctx, tenant, id)
		if err != nil {
			return err
		}
		ids := []string{id}
		if scope.InheritParent && scope.SubareaID != "" {
			var parent Scope
			if err := tx.Where("tenant_id = ? AND account_id = ? AND group_id = ? AND subarea_id = ''", tenant, scope.AccountID, scope.GroupID).First(&parent).Error; err != nil {
				return err
			}
			ids = append(ids, parent.ID)
		}
		var bindings []Binding
		// A deleted or transferred KB must not remain in the effective result.
		if err := tx.Table("octo_scope_bindings AS b").Select("b.*").Joins("JOIN knowledge_bases AS k ON k.id = b.knowledge_base_id AND k.tenant_id = b.tenant_id AND k.deleted_at IS NULL").Where("b.tenant_id = ? AND b.scope_id IN ?", tenant, ids).Order("b.knowledge_base_id, b.scope_id").Scan(&bindings).Error; err != nil {
			return err
		}
		positions := make(map[string]int)
		for _, b := range bindings {
			entry := EffectiveBinding{KnowledgeBaseID: b.KnowledgeBaseID, FromScopeID: b.ScopeID, Inherited: b.ScopeID != id}
			if pos, exists := positions[b.KnowledgeBaseID]; exists {
				if !entry.Inherited {
					result[pos] = entry
				}
				continue
			}
			positions[b.KnowledgeBaseID] = len(result)
			result = append(result, entry)
		}
		return nil
	})
	return result, err
}

type ScopeUse struct {
	ScopeID     string `json:"scope_id"`
	DisplayName string `json:"display_name"`
	AccountID   string `json:"account_id"`
	GroupID     string `json:"group_id"`
	SubareaID   string `json:"subarea_id"`
}

func (s *Store) Uses(ctx context.Context, tenant uint64, kb string) ([]ScopeUse, error) {
	if tenant == 0 {
		return nil, ErrInvalid
	}
	rows := []ScopeUse{}
	err := s.db.WithContext(ctx).Table("octo_scope_bindings AS b").Select("b.scope_id, s.display_name, s.account_id, s.group_id, s.subarea_id").Joins("JOIN octo_scopes AS s ON s.id = b.scope_id AND s.tenant_id = b.tenant_id").Where("b.tenant_id = ? AND b.knowledge_base_id = ?", tenant, kb).Order("s.account_id, s.group_id, s.subarea_id").Scan(&rows).Error
	return rows, err
}
