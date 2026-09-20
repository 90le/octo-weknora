package octo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

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
		adapter.receiptPolicy = scopedRuntimePolicy(db, adapter, channel.ID, channel.TenantID, account, connection.Token, true)
		adapter.db, adapter.channelID, adapter.tenantID = db, channel.ID, channel.TenantID
		runCtx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-runCtx.Done():
				return
			case <-time.After(time.Second):
			}
			adapter.recoverInbox(runCtx, handler)
			go adapter.retryDeliveries(runCtx)
			err := adapter.Run(runCtx, func(_ context.Context, msg *im.IncomingMessage) error {
				// A transient socket reconnect must not cancel already admitted QA.
				// The channel lifecycle still cancels work when the channel stops.
				err := adapter.accept(runCtx, msg, handler)
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
	return scopedRuntimePolicy(db, a, channelID, tenant, account, ciphertext, false)
}

// allowEmptyKnowledge is only used after proof of a completed deletion, never for Agent admission.
func scopedRuntimePolicy(db *gorm.DB, a *Adapter, channelID string, tenant uint64, account, ciphertext string, allowEmptyKnowledge bool) func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
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
			if !configuredUID(config, "allowed_dm_uids", msg.UserID) {
				return nil, im.ErrScopeDenied
			}
			ids := []string{}
			if raw, exists := config["dm_knowledge_base_ids"]; exists {
				values, ok := raw.([]interface{})
				if !ok {
					return nil, im.ErrScopeDenied
				}
				seen := map[string]bool{}
				for _, v := range values {
					id, ok := v.(string)
					if !ok || id == "" {
						return nil, im.ErrScopeDenied
					}
					if !seen[id] {
						ids = append(ids, id)
						seen[id] = true
					}
				}
			} else if channel.KnowledgeBaseID != "" {
				ids = append(ids, channel.KnowledgeBaseID)
			}
			if len(ids) == 0 {
				return nil, im.ErrScopeDenied
			}
			var count int64
			if db.WithContext(ctx).Table("knowledge_bases").Where("tenant_id = ? AND id IN ? AND deleted_at IS NULL", tenant, ids).Count(&count).Error != nil || count != int64(len(ids)) {
				return nil, im.ErrScopeDenied
			}
			result := &im.ExecutionScope{AccountID: account, ScopeID: "dm:" + msg.UserID, ScopeName: "私聊", KnowledgeBaseIDs: ids, Revision: fmt.Sprint(channel.ID, channel.AgentID, connection.UpdatedAt)}
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
			Role     json.RawMessage `json:"role"`
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
		canManage := false
		senderName := ""
		for _, member := range members {
			if member.UID != msg.UserID {
				continue
			}
			senderName = safeDisplayName(member.Name)
			if (string(member.Robot) == "0" || string(member.Robot) == "false") && (string(member.Role) == "1" || string(member.Role) == "2") {
				canManage = true
			}
			if flag(member.Robot) && (flag(member.BotAdmin) || (configuredUID(config, "management_bot_uids", member.UID) && configuredUID(config, "allowed_bot_uids", member.UID))) {
				canManage = true
			}
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
		managed := []string{}
		if canManage {
			managed, err = octointegration.NewStore(db).ManagedKnowledgeBases(ctx, tenant, stored.ID)
			if err != nil {
				return nil, im.ErrScopeDenied
			}
		}
		bindings, err := octointegration.NewStore(db).Effective(ctx, tenant, stored.ID)
		if err != nil || (len(bindings) == 0 && len(managed) == 0 && !allowEmptyKnowledge) {
			return nil, im.ErrScopeDenied
		}
		// Names and metadata refresh timestamps are not permission changes.
		// KB IDs participate in the native scope fingerprint separately.
		out := &im.ExecutionScope{ManageKnowledgeBaseIDs: managed, AccountID: account, ScopeID: stored.ID, ScopeName: stored.DisplayName, CanManageScope: canManage, AllowKnowledgeCreation: canManage && stored.AllowKnowledgeCreation, Revision: fmt.Sprint(channel.ID, channel.AgentID, connection.UpdatedAt, stored.ID), SenderName: senderName}
		for _, binding := range bindings {
			out.KnowledgeBaseIDs = append(out.KnowledgeBaseIDs, binding.KnowledgeBaseID)

		}
		out.ReadIssueScopeIDs = []string{stored.ID}
		if stored.SubareaID == "" && stored.AggregateChildIssues {
			var children []octointegration.Scope
			if db.WithContext(ctx).Where("tenant_id = ? AND account_id = ? AND group_id = ? AND subarea_id <> ''", tenant, account, stored.GroupID).Find(&children).Error != nil {
				return nil, im.ErrScopeDenied
			}
			for _, child := range children {
				out.ReadIssueScopeIDs = append(out.ReadIssueScopeIDs, child.ID)
				childBindings, e := octointegration.NewStore(a.db).Effective(ctx, a.tenantID, child.ID)
				if e != nil {
					return nil, im.ErrScopeDenied
				}
				for _, b := range childBindings {
					out.ReadIssueKnowledgeBaseIDs = append(out.ReadIssueKnowledgeBaseIDs, b.KnowledgeBaseID)
				}
			}
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
