package im

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

// withIMSandboxGrant binds a possible sandbox identity, never permission to
// deliver files or execute scripts. Each consumer must revalidate and check
// the pin immediately before every operation.
func (s *Service) withIMSandboxGrant(ctx context.Context, adapter Adapter, channel *IMChannel, msg *IncomingMessage, mapping *ChannelSession, session *types.Session, agent *types.CustomAgent, scope *ExecutionScope) context.Context {
	if s == nil || s.db == nil || s.agentService == nil || channel == nil || msg == nil || mapping == nil || session == nil || agent == nil || scope == nil ||
		channel.Platform != "octo" || msg.Platform != "octo" || (!scope.AllowFileDelivery && !scope.AllowSandboxShell && !scope.AllowSandboxSkills) ||
		agent.ID == "" || agent.ID != channel.AgentID || agent.TenantID == 0 || agent.TenantID != channel.TenantID ||
		mapping.SessionID != session.ID || session.TenantID != channel.TenantID {
		return ctx
	}
	wantPrincipal := types.Principal{Type: types.PrincipalIMUser, ID: fmt.Sprintf("%d:%s:%s:%s", channel.TenantID, channel.ID, msg.Platform, msg.UserID)}
	principal, ok := types.PrincipalFromContext(ctx)
	if !ok || principal != wantPrincipal {
		return ctx
	}
	configID := strings.TrimSpace(agent.Config.SandboxConfigID)
	if configID == "" || configID == types.SandboxConfigIDGlobalDefault || sandbox.IsHostSkillTarget(configID) {
		return ctx
	}
	raw, err := json.Marshal(agent.Config)
	if err != nil {
		return ctx
	}
	agentFingerprint := sha256.Sum256(raw)
	admitted := scopeFingerprint(scope)
	// Freeze identity material independently of any later mutation to the
	// queued message or its metadata map.
	msgCopy := *msg
	msgCopy.Extra = make(map[string]string, len(msg.Extra))
	for key, value := range msg.Extra {
		msgCopy.Extra[key] = value
	}
	if msg.Quote != nil {
		quote := *msg.Quote
		msgCopy.Quote = &quote
	}
	channelID, tenantID, agentID := channel.ID, channel.TenantID, agent.ID
	mappingID, sessionID := mapping.ID, session.ID
	grant := types.IMSandboxGrant{SessionID: sessionID, TenantID: tenantID, ConfigID: configID}
	grant.Revalidate = func(checkCtx context.Context) error {
		if p, ok := types.PrincipalFromContext(checkCtx); !ok || p != wantPrincipal {
			return ErrScopeDenied
		}
		if tid, ok := types.TenantIDFromContext(checkCtx); !ok || tid != tenantID {
			return ErrScopeDenied
		}
		if sid, ok := types.SessionIDFromContext(checkCtx); !ok || sid != sessionID {
			return ErrScopeDenied
		}
		var freshChannel IMChannel
		if err := s.db.WithContext(checkCtx).Where("id = ? AND tenant_id = ? AND platform = ? AND enabled = ? AND agent_id = ? AND mode = ? AND session_mode = ?",
			channelID, tenantID, "octo", true, agentID, "websocket", "user").First(&freshChannel).Error; err != nil {
			return ErrScopeDenied
		}
		current, err := authorizeExecution(checkCtx, adapter, &freshChannel, &msgCopy)
		if err != nil || current == nil || (!current.AllowFileDelivery && !current.AllowSandboxShell && !current.AllowSandboxSkills) || scopeFingerprint(current) != admitted {
			return ErrScopeDenied
		}
		var currentMapping ChannelSession
		if err := s.db.WithContext(checkCtx).Where("id = ? AND im_channel_id = ? AND tenant_id = ? AND platform = ? AND user_id = ? AND chat_id = ? AND agent_id = ? AND session_id = ? AND status = ?",
			mappingID, channelID, tenantID, "octo", msgCopy.UserID, msgCopy.ChatID, agentID, sessionID, "active").First(&currentMapping).Error; err != nil || !sessionScopeMatches(&currentMapping, current) {
			return ErrScopeDenied
		}
		var currentSession types.Session
		if err := s.db.WithContext(checkCtx).Where("id = ? AND tenant_id = ?", sessionID, tenantID).First(&currentSession).Error; err != nil {
			return ErrScopeDenied
		}
		currentAgent, err := s.agentService.GetAgentByID(checkCtx, agentID)
		if err != nil || currentAgent == nil || currentAgent.ID != agentID || currentAgent.TenantID != tenantID || strings.TrimSpace(currentAgent.Config.SandboxConfigID) != configID {
			return ErrScopeDenied
		}
		data, err := json.Marshal(currentAgent.Config)
		if err != nil || sha256.Sum256(data) != agentFingerprint {
			return ErrScopeDenied
		}
		return nil
	}
	return types.WithIMSandboxGrant(ctx, grant)
}
