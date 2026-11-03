package im

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Lock the channel row while selecting or inserting a mapping. The identity
// change trigger holds the same row lock while retiring existing mappings.
// Thus a message accepted by an old adapter cannot create a mapping for the
// new identity after that trigger has run.
func (s *Service) resolveOctoIdentitySession(ctx context.Context, msg *IncomingMessage, tenantID uint64, agentID, channelID string) (*ChannelSession, error) {
	type identity struct {
		AccountID string `json:"account_id"`
		BotUID    string `json:"bot_uid"`
	}
	var expected identity
	if !validWebOwnerIdentity(msg.octoIdentity) || json.Unmarshal(msg.octoIdentity, &expected) != nil {
		return nil, ErrScopeDenied
	}
	var result *ChannelSession
	selectOrCreate := func(created *types.Session) error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var channel struct {
				Credentials             string
				OwnerIdentityGeneration string
			}
			err := tx.Table("im_channels").Select("credentials, owner_identity_generation").Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND tenant_id = ? AND platform = ? AND deleted_at IS NULL", channelID, tenantID, "octo").Take(&channel).Error
			if err != nil {
				return err
			}
			var current identity
			if !validWebOwnerIdentity([]byte(channel.Credentials)) || json.Unmarshal([]byte(channel.Credentials), &current) != nil || current != expected || channel.OwnerIdentityGeneration != msg.octoIdentityGeneration {
				return ErrScopeDenied
			}
			var mapping ChannelSession
			err = tx.Where("platform = ? AND user_id = ? AND chat_id = ? AND tenant_id = ? AND agent_id = ? AND im_channel_id = ? AND deleted_at IS NULL", "octo", msg.UserID, msg.ChatID, tenantID, agentID, channelID).First(&mapping).Error
			if err == nil && sessionScopeMatches(&mapping, msg.executionScope) {
				result = &mapping
				return nil
			}
			if err == nil {
				if err = tx.Delete(&mapping).Error; err != nil {
					return err
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if created == nil {
				return nil
			}
			mapping = ChannelSession{Platform: "octo", UserID: msg.UserID, ChatID: msg.ChatID, SessionID: created.ID, TenantID: tenantID, AgentID: agentID, IMChannelID: channelID, Metadata: sessionScopeMetadata(msg.executionScope)}
			if err := tx.Create(&mapping).Error; err != nil {
				return err
			}
			result = &mapping
			return nil
		})
	}
	if err := selectOrCreate(nil); err != nil {
		return nil, err
	}
	if result != nil {
		return result, nil
	}
	created, err := s.sessionService.CreateSession(ctx, &types.Session{TenantID: tenantID, Title: imInitialSessionTitle(msg, buildUserSessionTitle), Description: "Auto-created from octo IM integration"})
	if err != nil {
		return nil, err
	}
	err = selectOrCreate(created)
	if err != nil || result == nil || result.SessionID != created.ID {
		if cleanupErr := s.db.WithContext(ctx).Where("id = ?", created.ID).Delete(created).Error; cleanupErr != nil {
			return nil, fmt.Errorf("cleanup unused session: %w", cleanupErr)
		}
	}
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrScopeDenied
	}
	return result, nil
}
