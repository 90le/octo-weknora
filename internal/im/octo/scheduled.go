package octo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
)

func (a *Adapter) reportPrincipal(ctx context.Context, s octobusiness.ReportSchedule) (octobusiness.Principal, error) {
	p := octobusiness.Principal{}
	if a.db == nil || s.TenantID != a.tenantID || s.ChannelID != a.channelID {
		return p, octobusiness.ReportAccessError("channel")
	}
	var channel im.IMChannel
	if a.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND enabled = ?", s.ChannelID, s.TenantID, true).First(&channel).Error != nil {
		return p, octobusiness.ReportAccessError("channel")
	}
	config, err := im.ParseCredentials(channel.Credentials)
	if err != nil || im.GetString(config, "bot_uid") != a.uid {
		return p, octobusiness.ReportAccessError("connection")
	}
	var connection octointegration.Connection
	account := im.GetString(config, "account_id")
	if a.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", s.TenantID, account).First(&connection).Error != nil {
		return p, octobusiness.ReportAccessError("connection")
	}
	secret, err := utils.DecryptStoredSecret(connection.Token)
	if err != nil || secret != a.api.token {
		return p, octobusiness.ReportAccessError("connection")
	}
	scope, err := octointegration.NewStore(a.db).Get(ctx, s.TenantID, s.ScopeID)
	if err != nil || scope.AccountID != account || scope.SyncStatus != "verified" ||
		scope.VerifiedAt == nil || scope.NameSource != "octo" {
		return p, octobusiness.ReportAccessError("scope")
	}
	endpoint := "/v1/bot/groups/" + scope.GroupID
	if scope.SubareaID != "" {
		endpoint += "/threads/" + scope.SubareaID
	}
	var current struct {
		Group  string `json:"group_no"`
		Thread string `json:"short_id"`
		Status int    `json:"status"`
	}
	if err = a.api.request(ctx, http.MethodGet, endpoint, nil, &current); err != nil || current.Group != scope.GroupID || current.Thread != scope.SubareaID || current.Status != 1 {
		return p, octobusiness.ReportAccessError("scope_live")
	}
	if s.RecipientType == "private" {
		dm, policyErr := im.ReadOctoPrivatePolicy(&channel, config)
		// Scheduled reports still require an explicit recipient and live group
		// membership. Neither creator nor friends mode adds report recipients.
		if policyErr != nil || !slices.Contains(dm.AllowedUIDs, s.RecipientUID) ||
			dm.AudienceDecision(s.RecipientUID, connection.VerifiedOwnerUID(a.uid), func() (bool, error) {
				return octointegration.CheckDMFriend(ctx, a.api.token, s.RecipientUID, a.api.http)
			}) != "allowed" {
			return p, octobusiness.ReportAccessError("recipient_policy")
		}
		memberPaths := []string{"/v1/bot/groups/" + scope.GroupID + "/members"}
		if scope.SubareaID != "" {
			memberPaths = append(memberPaths, endpoint+"/members")
		}
		for _, memberPath := range memberPaths {
			var members []struct {
				UID string `json:"uid"`
			}
			if a.api.request(ctx, http.MethodGet, memberPath, nil, &members) != nil {
				return p, octobusiness.ReportAccessError("recipient_membership")
			}
			found := false
			for _, m := range members {
				if m.UID == s.RecipientUID {
					found = true
				}
			}
			if !found {
				return p, octobusiness.ReportAccessError("recipient_membership")
			}
		}
	} else if s.RecipientType != "source" {
		return p, octobusiness.ReportAccessError("recipient_policy")
	}
	bindings, err := octointegration.NewStore(a.db).Effective(ctx, s.TenantID, scope.ID)
	if err != nil || len(bindings) == 0 {
		return p, octobusiness.ReportAccessError("binding")
	}
	p = octobusiness.Principal{TenantID: s.TenantID, AccountID: account, ChannelID: s.ChannelID, ScopeID: scope.ID, ScopeName: scope.DisplayName, GroupID: scope.GroupID, SubareaID: scope.SubareaID, UserID: "scheduled:" + s.ID, ReadIssueScopeIDs: []string{scope.ID}}
	for _, binding := range bindings {
		p.KnowledgeBaseIDs = append(p.KnowledgeBaseIDs, binding.KnowledgeBaseID)
	}
	if scope.SubareaID == "" && scope.AggregateChildIssues {
		var children []octointegration.Scope
		if err = a.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ? AND group_id = ? AND subarea_id <> ''", s.TenantID, account, scope.GroupID).Find(&children).Error; err != nil {
			return p, err
		}
		for _, child := range children {
			p.ReadIssueScopeIDs = append(p.ReadIssueScopeIDs, child.ID)
			childBindings, e := octointegration.NewStore(a.db).Effective(ctx, a.tenantID, child.ID)
			if e != nil {
				return p, im.ErrScopeDenied
			}
			for _, b := range childBindings {
				p.ReadIssueKnowledgeBaseIDs = append(p.ReadIssueKnowledgeBaseIDs, b.KnowledgeBaseID)
			}
		}
	}
	return p, nil
}

func (a *Adapter) ReportContext(ctx context.Context, s octobusiness.ReportSchedule) (context.Context, error) {
	p, err := a.reportPrincipal(ctx, s)
	if err != nil {
		return nil, err
	}
	p.Validate = func(c context.Context) (octobusiness.Principal, error) { return a.reportPrincipal(c, s) }
	return octobusiness.WithPrincipal(ctx, p), nil
}

func (a *Adapter) DeliverReport(ctx context.Context, s octobusiness.ReportSchedule, report *octobusiness.Report, file *octobusiness.ReportArtifact) error {
	p, err := a.reportPrincipal(ctx, s)
	if err != nil {
		return err
	}
	target, kind := p.GroupID, 2
	if p.SubareaID != "" {
		target += "____" + p.SubareaID
		kind = 5
	}
	if s.RecipientType == "private" {
		target = s.RecipientUID
		kind = 1
	}
	if _, err = wire.ParseScope(target, byte(kind)); err != nil {
		return err
	}
	msg := &im.IncomingMessage{UserID: s.RecipientUID, MessageID: s.RunKey, Extra: map[string]string{"octo_channel_id": target, "octo_channel_type": strconv.Itoa(kind)}}
	if file != nil {
		ctx, err = a.startFileDeliveryTurn(ctx, msg.MessageID)
		if err != nil {
			return err
		}
		return a.sendGeneratedFile(ctx, msg, file.Name, []byte(file.Content), file.ContentType, func() error { _, e := a.reportPrincipal(ctx, s); return e })
	}
	// The report generator includes explicit period, scope and count limits.
	markdown, err := octobusiness.ReportFile(report, "markdown")
	if err != nil {
		return err
	}
	if err := octobusiness.ValidateReportAuthority(ctx); err != nil {
		return err
	}
	var result struct {
		ID json.RawMessage `json:"message_id"`
	}
	err = a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{"channel_id": target, "channel_type": kind, "client_msg_no": uuid.NewSHA1(uuid.NameSpaceOID, []byte("report:"+s.RunKey)).String(), "payload": map[string]any{"type": 1, "content": markdown.Content}}, &result)
	if err != nil {
		return err
	}
	if wire.ReadID(result.ID) == "" {
		return fmt.Errorf("scheduled report delivery not acknowledged")
	}
	return nil
}

// ListBotDeliveryTargets enumerates all Octo groups and subareas joined by this
// Bot (including unbound management groups) and annotates KB binding counts.
func (a *Adapter) ResolveUserNames(ctx context.Context, tenant uint64, uids []string) map[string]string {
	out := map[string]string{}
	if a.tenantID != tenant || len(uids) == 0 {
		return out
	}
	now := time.Now()
	missing := make([]string, 0, len(uids))
	seen := map[string]bool{}

	a.profileMu.Lock()
	if a.profileCache == nil {
		a.profileCache = map[string]cachedOctoProfile{}
	}
	for _, raw := range uids {
		uid := strings.TrimSpace(raw)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		if entry, ok := a.profileCache[uid]; ok && now.Before(entry.expiresAt) {
			if entry.name != "" {
				out[uid] = entry.name
			}
			continue
		}
		if len(missing) < 40 {
			missing = append(missing, uid)
		}
	}
	a.profileMu.Unlock()

	if len(missing) == 0 {
		return out
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
	defer cancel()

	type resItem struct {
		uid  string
		name string
	}
	results := make(chan resItem, len(missing))
	sem := make(chan struct{}, 6)
	for _, uid := range missing {
		sem <- struct{}{}
		go func(u string) {
			defer func() { <-sem }()
			name := a.profileDisplayName(lookupCtx, u)
			if name == u {
				name = ""
			}
			results <- resItem{uid: u, name: name}
		}(uid)
	}

	a.profileMu.Lock()
	for i := 0; i < len(missing); i++ {
		r := <-results
		ttl := 15 * time.Minute
		if r.name == "" {
			ttl = 2 * time.Minute
		} else {
			out[r.uid] = r.name
		}
		a.profileCache[r.uid] = cachedOctoProfile{name: r.name, expiresAt: time.Now().Add(ttl)}
	}
	a.profileMu.Unlock()
	return out
}

func (a *Adapter) FindCachedUIDsByNickname(tenant uint64, keyword string) []string {
	if a.tenantID != tenant {
		return nil
	}
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return nil
	}
	now := time.Now()
	a.profileMu.Lock()
	defer a.profileMu.Unlock()
	var out []string
	for uid, entry := range a.profileCache {
		if entry.name != "" && now.Before(entry.expiresAt) && strings.Contains(strings.ToLower(entry.name), kw) {
			out = append(out, uid)
		}
	}
	return out
}

func (a *Adapter) ListBotDeliveryTargets(ctx context.Context, tenant uint64) (*octobusiness.BotDeliveryTargetsResponse, error) {
	if a.db == nil || tenant != a.tenantID {
		return nil, octobusiness.ErrDenied
	}
	var ch im.IMChannel
	if err := a.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND enabled = ?", a.channelID, tenant, true).First(&ch).Error; err != nil {
		return nil, octobusiness.ReportAccessError("channel")
	}
	cfg, _ := im.ParseCredentials(ch.Credentials)
	accountID := im.GetString(cfg, "account_id")

	var scopes []octointegration.Scope
	_ = a.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", tenant, accountID).Find(&scopes).Error
	scopeByKey := map[string]octointegration.Scope{}
	store := octointegration.NewStore(a.db)
	boundCount := map[string]int{}
	for _, sc := range scopes {
		k := sc.GroupID + ":" + sc.SubareaID
		scopeByKey[k] = sc
		if eff, err := store.Effective(ctx, tenant, sc.ID); err == nil {
			boundCount[k] = len(eff)
		}
	}

	var apiGroups []struct {
		GroupNo string `json:"group_no"`
		Name    string `json:"name"`
	}
	groups := []octobusiness.BotDeliveryGroup{}
	seen := map[string]bool{}
	if err := a.api.request(ctx, http.MethodGet, "/v1/bot/groups?page=1&page_size=100", nil, &apiGroups); err == nil {
		for _, g := range apiGroups {
			if g.GroupNo == "" {
				continue
			}
			k := g.GroupNo + ":"
			sc, hasScope := scopeByKey[k]
			label := strings.TrimSpace(g.Name)
			if label == "" && hasScope {
				label = sc.DisplayName
			}
			if label == "" {
				label = g.GroupNo
			}
			key := "group:" + g.GroupNo
			seen[key] = true
			groups = append(groups, octobusiness.BotDeliveryGroup{
				Key:          key,
				GroupID:      g.GroupNo,
				Kind:         "主群",
				Label:        label,
				BoundKBCount: boundCount[k],
			})
			var threads []struct {
				ShortID string `json:"short_id"`
				Name    string `json:"name"`
			}
			if a.api.request(ctx, http.MethodGet, "/v1/bot/groups/"+url.PathEscape(g.GroupNo)+"/threads?page=1&page_size=100", nil, &threads) == nil {
				for _, th := range threads {
					if th.ShortID == "" {
						continue
					}
					tk := g.GroupNo + ":" + th.ShortID
					tsc := scopeByKey[tk]
					tName := strings.TrimSpace(th.Name)
					if tName == "" {
						tName = th.ShortID
					}
					tKey := "group:" + g.GroupNo + ":" + th.ShortID
					seen[tKey] = true
					groups = append(groups, octobusiness.BotDeliveryGroup{
						Key:          tKey,
						GroupID:      g.GroupNo,
						SubareaID:    th.ShortID,
						Kind:         "子区",
						Label:        label + " / " + tName,
						BoundKBCount: boundCount[tk],
						Inherited:    tsc.InheritParent,
					})
				}
			}
		}
	}
	// Include locally verified scopes if live listing returned partial results.
	for _, sc := range scopes {
		key := "group:" + sc.GroupID
		kind := "主群"
		if sc.SubareaID != "" {
			key += ":" + sc.SubareaID
			kind = "子区"
		}
		if seen[key] {
			continue
		}
		k := sc.GroupID + ":" + sc.SubareaID
		groups = append(groups, octobusiness.BotDeliveryGroup{
			Key:          key,
			GroupID:      sc.GroupID,
			SubareaID:    sc.SubareaID,
			Kind:         kind,
			Label:        sc.DisplayName,
			BoundKBCount: boundCount[k],
			Inherited:    sc.InheritParent && sc.SubareaID != "",
		})
	}

	var dmCandidates []octobusiness.BotDMCandidate
	var conn octointegration.Connection
	if a.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", tenant, accountID).First(&conn).Error == nil {
		if owner := conn.VerifiedOwnerUID(a.uid); owner != "" {
			dmCandidates = append(dmCandidates, octobusiness.BotDMCandidate{
				UID:    owner,
				Name:   firstString(a.profileDisplayName(ctx, owner), owner),
				Source: "creator",
			})
		}
	}
	return &octobusiness.BotDeliveryTargetsResponse{
		ChannelID:    a.channelID,
		AccountID:    accountID,
		BotName:      ch.Name,
		Groups:       groups,
		DMCandidates: dmCandidates,
	}, nil
}

// VerifyBotFriend performs live Octo CheckDMFriend + user profile lookup for a UID.
func (a *Adapter) VerifyBotFriend(ctx context.Context, tenant uint64, uid string) (*octobusiness.BotFriendVerifyResult, error) {
	if a.db == nil || tenant != a.tenantID {
		return nil, octobusiness.ErrDenied
	}
	var ch im.IMChannel
	if err := a.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND enabled = ?", a.channelID, tenant, true).First(&ch).Error; err != nil {
		return nil, octobusiness.ReportAccessError("channel")
	}
	cfg, _ := im.ParseCredentials(ch.Credentials)
	accountID := im.GetString(cfg, "account_id")
	var conn octointegration.Connection
	owner := ""
	if a.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", tenant, accountID).First(&conn).Error == nil {
		owner = conn.VerifiedOwnerUID(a.uid)
	}
	name := a.profileDisplayName(ctx, uid)
	if owner != "" && uid == owner {
		return &octobusiness.BotFriendVerifyResult{
			OK:        true,
			UID:       uid,
			Name:      firstString(name, uid),
			IsCreator: true,
		}, nil
	}
	isFriend, err := octointegration.CheckDMFriend(ctx, a.api.token, uid, a.api.http)
	if err != nil {
		return &octobusiness.BotFriendVerifyResult{
			OK:            false,
			UID:           uid,
			Name:          name,
			ReasonCode:    "friend_check_unavailable",
			ReasonMessage: "Octo 好友校验接口暂不可用，请稍后重试",
		}, nil
	}
	if !isFriend {
		return &octobusiness.BotFriendVerifyResult{
			OK:            false,
			UID:           uid,
			Name:          name,
			ReasonCode:    "not_friend",
			ReasonMessage: fmt.Sprintf("UID %s 不是「%s」的好友，无法私聊投递", uid, ch.Name),
		}, nil
	}
	return &octobusiness.BotFriendVerifyResult{
		OK:   true,
		UID:  uid,
		Name: firstString(name, uid),
	}, nil
}

// DeliverReportToTarget verifies the target live on Octo and sends the rendered
// Markdown report to one specific group, subarea, or verified friend DM.
func (a *Adapter) DeliverReportToTarget(
	ctx context.Context, tenant uint64, target octobusiness.DeliveryTarget, runKey string, report *octobusiness.Report, file *octobusiness.ReportArtifact,
) (string, error) {
	if a.db == nil || tenant != a.tenantID {
		return "", octobusiness.ReportAccessError("channel")
	}
	var dest string
	var kind int
	switch target.Kind {
	case "dm":
		ver, err := a.VerifyBotFriend(ctx, tenant, target.UID)
		if err != nil || ver == nil || !ver.OK {
			return "", octobusiness.ReportAccessError("recipient_policy")
		}
		dest, kind = target.UID, 1
	case "group":
		endpoint := "/v1/bot/groups/" + url.PathEscape(target.GroupID)
		dest, kind = target.GroupID, 2
		if target.SubareaID != "" {
			endpoint += "/threads/" + url.PathEscape(target.SubareaID)
			dest = target.GroupID + "____" + target.SubareaID
			kind = 5
		}
		var live struct {
			Group  string `json:"group_no"`
			Thread string `json:"short_id"`
			Status int    `json:"status"`
		}
		if err := a.api.request(ctx, http.MethodGet, endpoint, nil, &live); err != nil || live.Group != target.GroupID || live.Thread != target.SubareaID || live.Status != 1 {
			return "", octobusiness.ReportAccessError("scope_live")
		}
	default:
		return "", octobusiness.ErrInvalid
	}
	if _, err := wire.ParseScope(dest, byte(kind)); err != nil {
		return "", octobusiness.ErrInvalid
	}
	if file != nil {
		msg := &im.IncomingMessage{UserID: target.UID, MessageID: runKey, Extra: map[string]string{"octo_channel_id": dest, "octo_channel_type": strconv.Itoa(kind)}}
		fctx, err := a.startFileDeliveryTurn(ctx, msg.MessageID)
		if err != nil {
			return "", err
		}
		if err := a.sendGeneratedFile(fctx, msg, file.Name, []byte(file.Content), file.ContentType, func() error { return nil }); err != nil {
			return "", err
		}
		return "file:" + runKey, nil
	}
	markdown, err := octobusiness.ReportFile(report, "markdown")
	if err != nil {
		return "", err
	}
	var result struct {
		ID json.RawMessage `json:"message_id"`
	}
	err = a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{
		"channel_id":    dest,
		"channel_type":  kind,
		"client_msg_no": uuid.NewSHA1(uuid.NameSpaceOID, []byte("report-mt:"+runKey)).String(),
		"payload":       map[string]any{"type": 1, "content": markdown.Content},
	}, &result)
	if err != nil {
		return "", err
	}
	rcpt := wire.ReadID(result.ID)
	if rcpt == "" {
		return "", fmt.Errorf("scheduled report delivery not acknowledged")
	}
	return rcpt, nil
}

func firstString(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
