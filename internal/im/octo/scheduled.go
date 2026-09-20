package octo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

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
		return p, im.ErrScopeDenied
	}
	var channel im.IMChannel
	if a.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND enabled = ?", s.ChannelID, s.TenantID, true).First(&channel).Error != nil {
		return p, im.ErrScopeDenied
	}
	config, err := im.ParseCredentials(channel.Credentials)
	if err != nil || im.GetString(config, "bot_uid") != a.uid {
		return p, im.ErrScopeDenied
	}
	var connection octointegration.Connection
	account := im.GetString(config, "account_id")
	if a.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", s.TenantID, account).First(&connection).Error != nil {
		return p, im.ErrScopeDenied
	}
	secret, err := utils.DecryptStoredSecret(connection.Token)
	if err != nil || secret != a.api.token {
		return p, im.ErrScopeDenied
	}
	scope, err := octointegration.NewStore(a.db).Get(ctx, s.TenantID, s.ScopeID)
	if err != nil || scope.AccountID != account || scope.SyncStatus != "verified" {
		return p, im.ErrScopeDenied
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
		return p, im.ErrScopeDenied
	}
	if s.RecipientType == "private" {
		if !configuredUID(config, "allowed_dm_uids", s.RecipientUID) {
			return p, im.ErrScopeDenied
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
				return p, im.ErrScopeDenied
			}
			found := false
			for _, m := range members {
				if m.UID == s.RecipientUID {
					found = true
				}
			}
			if !found {
				return p, im.ErrScopeDenied
			}
		}
	} else if s.RecipientType != "source" {
		return p, im.ErrScopeDenied
	}
	bindings, err := octointegration.NewStore(a.db).Effective(ctx, s.TenantID, scope.ID)
	if err != nil || len(bindings) == 0 {
		return p, im.ErrScopeDenied
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
