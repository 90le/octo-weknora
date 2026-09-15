package octo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/utils"
	"gorm.io/gorm"
)

// NewFactory reuses the workspace's encrypted Octo connection. IM credentials
// contain references (account_id, bot_uid), not a second copy of its Bot Token.
func NewFactory(db *gorm.DB) im.AdapterFactory {
	return func(ctx context.Context, channel *im.IMChannel, handler func(context.Context, *im.IncomingMessage) error) (im.Adapter, context.CancelFunc, error) {
		if db == nil || channel.SessionMode == string(im.SessionModeThread) || im.ResolveMode(channel, "websocket") != "websocket" {
			return nil, nil, errors.New("Octo requires database, websocket and per-user sessions")
		}
		config, err := im.ParseCredentials(channel.Credentials)
		if err != nil {
			return nil, nil, err
		}
		account, uid := im.GetString(config, "account_id"), im.GetString(config, "bot_uid")
		if account == "" || uid == "" {
			return nil, nil, errors.New("Octo connection and Bot UID required")
		}
		var connection octointegration.Connection
		if err = db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", channel.TenantID, account).First(&connection).Error; err != nil {
			return nil, nil, errors.New("Octo connection not found in this workspace")
		}
		if !strings.HasPrefix(connection.Token, utils.EncPrefix) {
			return nil, nil, errors.New("Octo connection must be encrypted")
		}
		token, err := utils.DecryptStoredSecret(connection.Token)
		if err != nil {
			return nil, nil, errors.New("Octo connection cannot be decrypted")
		}
		adapter, err := NewAdapter(token, uid)
		if err != nil {
			return nil, nil, err
		}
		reg, err := adapter.api.register(ctx)
		if err != nil {
			return nil, nil, err
		}
		if reg.UID != uid {
			return nil, nil, errors.New("Octo connection Bot UID mismatch")
		}
		adapter.policy = runtimePolicy(db, adapter, channel.ID, channel.TenantID, account, connection.Token)
		runCtx, cancel := context.WithCancel(context.Background())
		go func() {
			err := adapter.Run(runCtx, func(callCtx context.Context, msg *im.IncomingMessage) error {
				err := handler(callCtx, msg)
				if errors.Is(err, im.ErrScopeDenied) {
					return nil
				} // intentional silent rejection
				return err
			})
			if err != nil && runCtx.Err() == nil {
				logger.Errorf(context.Background(), "[IM] Octo channel %s stopped: %v", channel.ID, err)
			}
		}()
		return adapter, cancel, nil
	}
}

func runtimePolicy(db *gorm.DB, a *Adapter, channelID string, tenant uint64, account, ciphertext string) func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
	return func(ctx context.Context, _ *im.IMChannel, msg *im.IncomingMessage) (*im.ExecutionScope, error) {
		if msg == nil || msg.Platform != Platform {
			return nil, im.ErrScopeDenied
		}
		var channel im.IMChannel
		if db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND enabled = ?", channelID, tenant, true).First(&channel).Error != nil {
			return nil, im.ErrScopeDenied
		}
		config, err := im.ParseCredentials(channel.Credentials)
		if err != nil || im.GetString(config, "account_id") != account || im.GetString(config, "bot_uid") != a.uid {
			return nil, im.ErrScopeDenied
		}
		var connection octointegration.Connection
		if db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", tenant, account).First(&connection).Error != nil || connection.Token != ciphertext {
			return nil, im.ErrScopeDenied
		}
		// The first release is group/subarea knowledge access. DM admission is
		// explicit and opt-in by native UID; an empty allowlist denies all DMs.
		if msg.ChatType == im.ChatTypeDirect {
			if !configuredUID(config, "allowed_dm_uids", msg.UserID) || channel.KnowledgeBaseID == "" {
				return nil, im.ErrScopeDenied
			}
			var count int64
			if db.WithContext(ctx).Table("knowledge_bases").Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", tenant, channel.KnowledgeBaseID).Count(&count).Error != nil || count != 1 {
				return nil, im.ErrScopeDenied
			}
			result := &im.ExecutionScope{KnowledgeBaseIDs: []string{channel.KnowledgeBaseID}, Revision: fmt.Sprint(channel.ID, channel.AgentID, connection.UpdatedAt)}
			var profile struct {
				UID  string `json:"uid"`
				Name string `json:"name"`
			}
			if a.api.request(ctx, http.MethodGet, "/v1/bot/user/info?uid="+url.QueryEscape(msg.UserID), nil, &profile) == nil && profile.UID == msg.UserID {
				result.SenderName = safeDisplayName(profile.Name)
			}
			return result, nil
		}
		kind := byte(2)
		if msg.Extra["octo_subarea_id"] != "" {
			kind = 5
		}
		scope, err := wire.ParseScope(msg.Extra["octo_channel_id"], kind)
		if err != nil {
			return nil, im.ErrScopeDenied
		}
		if msg.ChatID != scope.Channel || msg.Extra["octo_group_id"] != scope.Group || msg.Extra["octo_subarea_id"] != scope.Subarea {
			return nil, im.ErrScopeDenied
		}
		var stored octointegration.Scope
		if db.WithContext(ctx).Where("tenant_id = ? AND account_id = ? AND group_id = ? AND subarea_id = ?", tenant, account, scope.Group, scope.Subarea).First(&stored).Error != nil {
			return nil, im.ErrScopeDenied
		}
		if stored.VerifiedAt == nil || stored.NameSource != "octo" || stored.SyncStatus != "verified" {
			return nil, im.ErrScopeDenied
		}
		var members []struct {
			UID      string          `json:"uid"`
			Name     string          `json:"name"`
			Robot    json.RawMessage `json:"robot"`
			BotAdmin json.RawMessage `json:"bot_admin"`
		}
		path := "/v1/bot/groups/" + scope.Group
		if a.api.request(ctx, http.MethodGet, path+"/members", nil, &members) != nil {
			return nil, im.ErrScopeDenied
		}
		if scope.Subarea != "" {
			var childMembers []struct {
				UID string `json:"uid"`
			}
			if a.api.request(ctx, http.MethodGet, path+"/threads/"+scope.Subarea+"/members", nil, &childMembers) != nil {
				return nil, im.ErrScopeDenied
			}
			found := false
			for _, member := range childMembers {
				if member.UID == msg.UserID {
					found = true
				}
			}
			if !found {
				return nil, im.ErrScopeDenied
			}
		}
		admitted := false
		senderName := ""
		for _, member := range members {
			if member.UID != msg.UserID {
				continue
			}
			senderName = safeDisplayName(member.Name)
			if string(member.Robot) == "0" || string(member.Robot) == "false" {
				admitted = true
			}
			if flag(member.Robot) && (flag(member.BotAdmin) || configuredUID(config, "allowed_bot_uids", member.UID)) {
				admitted = true
			}
		}
		if !admitted {
			return nil, im.ErrScopeDenied
		}
		if msg.Extra["octo_addressed"] != "true" && (msg.Quote == nil || !msg.Quote.IsBotMessage) {
			var pref struct {
				Effective json.RawMessage `json:"effective"`
			}
			if a.api.request(ctx, http.MethodGet, "/v1/bot/groups/"+scope.Group+"/mention_pref", nil, &pref) != nil || !flag(pref.Effective) {
				return nil, im.ErrScopeDenied
			}
		}
		bindings, err := octointegration.NewStore(db).Effective(ctx, tenant, stored.ID)
		if err != nil || len(bindings) == 0 {
			return nil, im.ErrScopeDenied
		}
		// Names and metadata refresh timestamps are not permission changes.
		// KB IDs participate in the native scope fingerprint separately.
		out := &im.ExecutionScope{Revision: fmt.Sprint(channel.ID, channel.AgentID, connection.UpdatedAt, stored.ID), SenderName: senderName}
		for _, binding := range bindings {
			out.KnowledgeBaseIDs = append(out.KnowledgeBaseIDs, binding.KnowledgeBaseID)
		}
		return out, nil
	}
}

func flag(value json.RawMessage) bool { return string(value) == "1" || string(value) == "true" }
func safeDisplayName(name string) string {
	name = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(name))
	runes := []rune(name)
	if len(runes) > 128 {
		return string(runes[:128])
	}
	return name
}
func configuredUID(config map[string]interface{}, key, uid string) bool {
	values, ok := config[key].([]interface{})
	if !ok {
		return false
	}
	for _, value := range values {
		if s, ok := value.(string); ok && s == uid {
			return true
		}
	}
	return false
}
