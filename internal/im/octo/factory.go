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
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/utils"
	"gorm.io/gorm"
)

// NewFactory reuses the workspace's encrypted Octo connection. IM credentials
// contain references (account_id, bot_uid), not a second copy of its Bot Token.
func NewFactory(db *gorm.DB, services ...*octobusiness.Service) im.AdapterFactory {
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
		if len(services) > 0 {
			adapter.business = services[0]
		}
		reg, err := adapter.api.register(ctx)
		if err != nil {
			return nil, nil, err
		}
		if reg.UID != uid {
			return nil, nil, errors.New("Octo connection Bot UID mismatch")
		}
		// Registration is a receiver lifecycle operation, never a page read.
		// Persist its trusted result only for the credential we just verified.
		if err = octointegration.NewStore(db).RecordVerifiedIdentity(ctx, channel.TenantID, account, connection.Token, octointegration.ConnectionIdentity{BotUID: reg.UID, Name: reg.Name, OwnerUID: reg.OwnerUID}); err != nil {
			return nil, nil, err
		}
		adapter.policy = runtimePolicy(db, adapter, channel.ID, channel.TenantID, account, connection.Token)
		adapter.receiptPolicy = scopedRuntimePolicy(db, adapter, channel.ID, channel.TenantID, account, connection.Token, true)
		adapter.db, adapter.channelID, adapter.tenantID = db, channel.ID, channel.TenantID
		runCtx, cancel := context.WithCancel(context.Background())
		routedHandler := adapter.confirmationHandler(handler)
		go func() {
			select {
			case <-runCtx.Done():
				return
			case <-time.After(time.Second):
			}
			adapter.recoverInbox(runCtx, routedHandler)
			go adapter.runConfirmationEvents(runCtx, routedHandler)
			go adapter.runConfirmationCardReconciliation(runCtx)
			go func() {
				adapter.recoverProgressCards(runCtx)
				ticker := time.NewTicker(5 * time.Minute)
				defer ticker.Stop()
				for {
					select {
					case <-runCtx.Done():
						return
					case <-ticker.C:
						adapter.recoverProgressCards(runCtx)
					}
				}
			}()
			go adapter.retryDeliveries(runCtx)
			go adapter.runGroupQuestionSync(runCtx, account, routedHandler)
			err := adapter.Run(runCtx, func(_ context.Context, msg *im.IncomingMessage) error {
				// A transient socket reconnect must not cancel already admitted QA.
				// The channel lifecycle still cancels work when the channel stops.
				err := adapter.accept(runCtx, msg, routedHandler)
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
		if msg.ChatType == im.ChatTypeDirect {
			p, err := im.ReadOctoPrivatePolicy(&channel, config)
			if err != nil || p.Decision(msg.UserID, connection.VerifiedOwnerUID(a.uid), func() (bool, error) {
				return octointegration.CheckDMFriend(ctx, a.api.token, msg.UserID, a.api.http)
			}) != "allowed" {
				return nil, im.ErrScopeDenied
			}
			ids := p.KnowledgeIDs
			var count int64
			if db.WithContext(ctx).Table("knowledge_bases").Where("tenant_id = ? AND id IN ? AND deleted_at IS NULL", tenant, ids).Count(&count).Error != nil || count != int64(len(ids)) {
				return nil, im.ErrScopeDenied
			}
			result := &im.ExecutionScope{
				AccountID: account, ScopeID: "dm:" + msg.UserID, ScopeName: "私聊", KnowledgeBaseIDs: ids,
				AllowGeneratedFiles: p.Tools.Legacy,
				AllowFileDelivery:   p.Tools.Files,
				AllowSandboxShell:   p.Tools.Shell,
				AllowSandboxSkills:  p.Tools.Skills,
				Revision:            fmt.Sprint(channel.ID, channel.AgentID, connection.UpdatedAt),
			}
			result.SenderName = a.profileDisplayName(ctx, msg.UserID)
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
		if stored.VerifiedAt == nil || stored.NameSource != "octo" || stored.SyncStatus != "verified" ||
			!stored.MemberQuestionsEnabled {
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
			switch {
			case string(member.Robot) == "0" || string(member.Robot) == "false":
				admitted = true
				canManage = string(member.Role) == "1" || string(member.Role) == "2"
			case flag(member.Robot) && configuredUID(config, "allowed_bot_uids", member.UID):
				admitted = true
				canManage = flag(member.BotAdmin) || configuredUID(config, "management_bot_uids", member.UID)
			}
			break
		}
		if !admitted {
			return nil, im.ErrScopeDenied
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
		out := &im.ExecutionScope{
			ManageKnowledgeBaseIDs: managed, AccountID: account, ScopeID: stored.ID, ScopeName: stored.DisplayName,
			CanManageScope: canManage, AllowKnowledgeCreation: canManage && stored.AllowKnowledgeCreation,
			AllowPublicWeb: stored.AllowPublicWeb, AllowGeneratedFiles: im.GetBool(config, "allow_generated_files"),
			AllowFileDelivery: im.GetExplicitBool(config, "allow_file_delivery"),
			AllowSandboxShell: im.GetExplicitBool(
				config,
				"allow_sandbox_shell",
			), AllowSandboxSkills: im.GetExplicitBool(config, "allow_sandbox_skills"),
			Revision: fmt.Sprint(channel.ID, channel.AgentID, connection.UpdatedAt, stored.ID), SenderName: senderName,
		}
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

func (a *Adapter) profileDisplayName(ctx context.Context, uid string) string {
	var profile struct {
		UID  string `json:"uid"`
		Name string `json:"name"`
	}
	if a.api.request(ctx, http.MethodGet, "/v1/bot/user/info?uid="+url.QueryEscape(uid), nil, &profile) != nil || profile.UID != uid {
		return ""
	}
	return safeDisplayName(profile.Name)
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
