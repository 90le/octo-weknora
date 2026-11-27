package im

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type octoSessionBridgeAdapter interface {
	ResolveUserNames(context.Context, uint64, []string) map[string]string
	SendSessionBridgeReply(ctx context.Context, tenant uint64, targetChannelID string, channelType int, body string, quoteInboxInput string, mentionUID string, mentionName string) (string, error)
	SendSessionBridgeFile(ctx context.Context, tenant uint64, targetChannelID string, channelType int, fileName string, data []byte, contentType string, quoteInboxInput string) (string, string, error)
}

// OctoSessionBridgeService bridges Web sessions with their originating Octo
// channel, resolving human-readable provenance (chat type, group/subarea,
// nickname) and routing Web replies & files back along the latest return path.
type OctoSessionBridgeService struct {
	im *Service
}

func (s *Service) OctoSessionBridge() *OctoSessionBridgeService {
	return &OctoSessionBridgeService{im: s}
}

func (b *OctoSessionBridgeService) lookupAdapter(tenant uint64, channelID string) (octoSessionBridgeAdapter, *IMChannel, error) {
	if b == nil || b.im == nil {
		return nil, nil, errors.New("im service unavailable")
	}
	a, ch, ok := b.im.GetChannelAdapter(channelID)
	if !ok || ch == nil || ch.TenantID != tenant || ch.Platform != "octo" || !ch.Enabled {
		return nil, nil, errors.New("octo channel unavailable or disabled")
	}
	bridge, ok := a.(octoSessionBridgeAdapter)
	if !ok {
		return nil, nil, errors.New("octo adapter does not support session bridge")
	}
	return bridge, ch, nil
}

type sessionRoute struct {
	origin          *types.SessionIMOrigin
	channelType     int    // 1=DM, 2=group, 5=subarea
	targetChannelID string // Octo channel_id for /v1/bot/sendMessage
	quoteInboxInput string
}

func (b *OctoSessionBridgeService) resolveRoutes(ctx context.Context, tenant uint64, sessionIDs []string) map[string]*sessionRoute {
	out := map[string]*sessionRoute{}
	if b == nil || b.im == nil || b.im.db == nil || len(sessionIDs) == 0 {
		return out
	}
	quiet := b.im.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)

	var mappings []struct {
		SessionID   string
		IMChannelID string
		Platform    string
		UserID      string
		ChatID      string
		ThreadID    string
	}
	if err := quiet.Table("im_channel_sessions").
		Select("session_id, im_channel_id, platform, user_id, chat_id, thread_id").
		Where("tenant_id = ? AND platform = 'octo' AND session_id IN ?", tenant, sessionIDs).
		Order("updated_at DESC").
		Find(&mappings).Error; err != nil || len(mappings) == 0 {
		return out
	}

	bySession := map[string]struct {
		IMChannelID, UserID, ChatID, ThreadID string
	}{}
	channelIDs := map[string]bool{}
	for _, m := range mappings {
		if _, exists := bySession[m.SessionID]; !exists {
			bySession[m.SessionID] = struct {
				IMChannelID, UserID, ChatID, ThreadID string
			}{m.IMChannelID, m.UserID, m.ChatID, m.ThreadID}
			if m.IMChannelID != "" {
				channelIDs[m.IMChannelID] = true
			}
		}
	}

	chNames := map[string]string{}
	if len(channelIDs) > 0 {
		ids := make([]string, 0, len(channelIDs))
		for id := range channelIDs {
			ids = append(ids, id)
		}
		var chs []struct {
			ID, Name string
		}
		if quiet.Table("im_channels").Select("id, name").
			Where("tenant_id = ? AND id IN ?", tenant, ids).Find(&chs).Error == nil {
			for _, ch := range chs {
				chNames[ch.ID] = ch.Name
			}
		}
	}

	scopeNames := map[string]string{}
	var scopes []struct {
		GroupID, SubareaID, DisplayName string
	}
	if quiet.Table("octo_scopes").Select("group_id, subarea_id, display_name").
		Where("tenant_id = ?", tenant).Find(&scopes).Error == nil {
		for _, sc := range scopes {
			scopeNames[sc.GroupID+":"+sc.SubareaID] = sc.DisplayName
		}
	}

	latestReqBySession := map[string]string{}
	var msgs []struct {
		SessionID string
		RequestID string
	}
	if quiet.Table("messages").Select("session_id, request_id").
		Where("session_id IN ? AND request_id LIKE 'octo:%'", sessionIDs).
		Order("created_at DESC").Find(&msgs).Error == nil {
		for _, m := range msgs {
			if _, ok := latestReqBySession[m.SessionID]; !ok {
				latestReqBySession[m.SessionID] = m.RequestID
			}
		}
	}

	inboxByMsgID := map[string]string{}
	if len(latestReqBySession) > 0 {
		reqIDs := make([]string, 0, len(latestReqBySession))
		for _, rid := range latestReqBySession {
			reqIDs = append(reqIDs, rid)
		}
		var inboxRows []struct {
			MessageID string
			Input     string
		}
		if quiet.Table("octo_message_inbox").Select("message_id, input").
			Where("tenant_id = ? AND message_id IN ?", tenant, reqIDs).Find(&inboxRows).Error == nil {
			for _, r := range inboxRows {
				inboxByMsgID[r.MessageID] = r.Input
			}
		}
	}

	uidsByChannel := map[string][]string{}
	for sid, m := range bySession {
		rt := &sessionRoute{
			origin: &types.SessionIMOrigin{
				Platform:    "octo",
				ChannelID:   m.IMChannelID,
				ChannelName: chNames[m.IMChannelID],
				UserUID:     m.UserID,
			},
		}

		// 1. Prefer exact return path from latest octo_message_inbox entry
		if reqID := latestReqBySession[sid]; reqID != "" {
			if rawInput := inboxByMsgID[reqID]; rawInput != "" {
				var in IncomingMessage
				if json.Unmarshal([]byte(rawInput), &in) == nil && in.UserID != "" {
					rt.quoteInboxInput = rawInput
					rt.origin.UserUID = in.UserID
					if in.UserName != "" && in.UserName != in.UserID {
						rt.origin.UserName = in.UserName
					}
					kind, _ := strconv.Atoi(in.Extra["octo_channel_type"])
					if kind == 1 || in.ChatType == ChatTypeDirect {
						rt.channelType = 1
						rt.targetChannelID = in.UserID
						rt.origin.ChatType = "direct"
						rt.origin.SourceName = "私聊"
					} else if kind == 5 || strings.Contains(in.Extra["octo_channel_id"], "____") {
						rt.channelType = 5
						rt.targetChannelID = in.Extra["octo_channel_id"]
						rt.origin.ChatType = "subarea"
						rt.origin.GroupID = in.Extra["octo_group_id"]
						rt.origin.SubareaID = in.Extra["octo_subarea_id"]
						if rt.origin.GroupID == "" && strings.Contains(rt.targetChannelID, "____") {
							parts := strings.SplitN(rt.targetChannelID, "____", 2)
							rt.origin.GroupID, rt.origin.SubareaID = parts[0], parts[1]
						}
					} else if kind == 2 {
						rt.channelType = 2
						rt.targetChannelID = in.Extra["octo_channel_id"]
						rt.origin.ChatType = "group"
						rt.origin.GroupID = in.Extra["octo_group_id"]
					}
				}
			}
		}

		// 2. Fallback to im_channel_sessions columns (Octo subareas use 4 underscores "____")
		if rt.channelType == 0 {
			if m.ChatID == "" || m.ChatID == m.UserID || strings.HasPrefix(m.ChatID, "dm:") {
				rt.channelType = 1
				rt.targetChannelID = m.UserID
				rt.origin.ChatType = "direct"
				rt.origin.SourceName = "私聊"
			} else if strings.Contains(m.ChatID, "____") {
				parts := strings.SplitN(m.ChatID, "____", 2)
				rt.channelType = 5
				rt.targetChannelID = m.ChatID
				rt.origin.ChatType = "subarea"
				rt.origin.GroupID = parts[0]
				rt.origin.SubareaID = parts[1]
			} else if m.ThreadID != "" {
				rt.channelType = 5
				rt.targetChannelID = m.ChatID + "____" + m.ThreadID
				rt.origin.ChatType = "subarea"
				rt.origin.GroupID = m.ChatID
				rt.origin.SubareaID = m.ThreadID
			} else {
				rt.channelType = 2
				rt.targetChannelID = m.ChatID
				rt.origin.ChatType = "group"
				rt.origin.GroupID = m.ChatID
			}
		}

		if rt.origin.ChatType == "subarea" || rt.origin.ChatType == "group" {
			subName := scopeNames[rt.origin.GroupID+":"+rt.origin.SubareaID]
			parentName := scopeNames[rt.origin.GroupID+":"]
			if rt.origin.ChatType == "subarea" {
				switch {
				case parentName != "" && subName != "" && !strings.Contains(subName, parentName):
					rt.origin.SourceName = parentName + " / " + subName
				case subName != "":
					rt.origin.SourceName = subName
				case parentName != "":
					rt.origin.SourceName = parentName + " / 子区"
				default:
					rt.origin.SourceName = "子区 " + rt.origin.SubareaID
				}
			} else {
				if parentName != "" {
					rt.origin.SourceName = parentName
				} else {
					rt.origin.SourceName = rt.origin.GroupID
				}
			}
		}

		if rt.origin.UserUID != "" && (rt.origin.UserName == "" || rt.origin.UserName == rt.origin.UserUID) {
			uidsByChannel[m.IMChannelID] = append(uidsByChannel[m.IMChannelID], rt.origin.UserUID)
		}
		out[sid] = rt
	}

	resolvedNames := map[string]string{}
	for chID, uids := range uidsByChannel {
		if adapter, _, err := b.lookupAdapter(tenant, chID); err == nil {
			for uid, nick := range adapter.ResolveUserNames(ctx, tenant, uids) {
				if nick != "" {
					resolvedNames[uid] = nick
				}
			}
		}
	}
	for _, rt := range out {
		if nick := resolvedNames[rt.origin.UserUID]; nick != "" {
			rt.origin.UserName = nick
		}
	}
	return out
}

// ResolveSessionOrigins implements session.OctoSessionBridge.
func (b *OctoSessionBridgeService) ResolveSessionOrigins(ctx context.Context, tenantID uint64, sessionIDs []string) map[string]*types.SessionIMOrigin {
	routes := b.resolveRoutes(ctx, tenantID, sessionIDs)
	out := make(map[string]*types.SessionIMOrigin, len(routes))
	for sid, rt := range routes {
		out[sid] = rt.origin
	}
	return out
}

// SendToSessionOrigin implements session.OctoSessionBridge.
func (b *OctoSessionBridgeService) SendToSessionOrigin(
	ctx context.Context, tenantID uint64, sessionID, content string, quoteLatest bool,
) (string, *types.SessionIMOrigin, error) {
	routes := b.resolveRoutes(ctx, tenantID, []string{sessionID})
	rt, ok := routes[sessionID]
	if !ok || rt == nil || rt.origin == nil {
		return "", nil, errors.New("octo session origin not found")
	}
	adapter, _, err := b.lookupAdapter(tenantID, rt.origin.ChannelID)
	if err != nil {
		return "", rt.origin, err
	}
	quoteRaw := ""
	if quoteLatest {
		quoteRaw = rt.quoteInboxInput
	}
	receipt, err := adapter.SendSessionBridgeReply(
		ctx, tenantID, rt.targetChannelID, rt.channelType, content, quoteRaw,
		rt.origin.UserUID, rt.origin.UserName,
	)
	if err != nil {
		return "", rt.origin, err
	}
	return receipt, rt.origin, nil
}

// SendFileToSessionOrigin implements session.OctoSessionBridge.
func (b *OctoSessionBridgeService) SendFileToSessionOrigin(
	ctx context.Context, tenantID uint64, sessionID, fileName string, data []byte, contentType string, quoteLatest bool,
) (string, string, *types.SessionIMOrigin, error) {
	routes := b.resolveRoutes(ctx, tenantID, []string{sessionID})
	rt, ok := routes[sessionID]
	if !ok || rt == nil || rt.origin == nil {
		return "", "", nil, errors.New("octo session origin not found")
	}
	adapter, _, err := b.lookupAdapter(tenantID, rt.origin.ChannelID)
	if err != nil {
		return "", "", rt.origin, err
	}
	quoteRaw := ""
	if quoteLatest {
		quoteRaw = rt.quoteInboxInput
	}
	receipt, sentName, err := adapter.SendSessionBridgeFile(
		ctx, tenantID, rt.targetChannelID, rt.channelType, fileName, data, contentType, quoteRaw,
	)
	if err != nil {
		return "", sentName, rt.origin, err
	}
	return receipt, sentName, rt.origin, nil
}
