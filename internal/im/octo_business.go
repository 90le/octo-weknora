package im

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/octobusiness"
)

type generatedFileSender interface {
	SendGeneratedFile(context.Context, *IncomingMessage, string, []byte, string) error
}

// Business identity is built from adapter-verified native facts, never tool
// arguments. Every operation resolves current scope and membership again.
func (s *Service) businessContext(ctx context.Context, req *qaRequest, scope *ExecutionScope) context.Context {
	if scope == nil || req.channel.Platform != "octo" {
		return ctx
	}
	principal := func(current *ExecutionScope) octobusiness.Principal {
		text := req.msg.Content
		// A mention label is display text only. Removing the exact Bot label
		// cannot grant a role; confirmation remains tied to the native sender.
		if commandText, ok := req.msg.Extra["octo_command_text"]; ok {
			text = commandText
		} else if req.msg.Extra["octo_addressed"] == "true" {
			for _, prefix := range []string{"@" + req.channel.Name, "@" + GetStringFromChannel(req.channel, "bot_uid")} {
				if strings.HasPrefix(text, prefix) {
					text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
					break
				}
			}
		}
		p := octobusiness.Principal{CanCreateKnowledgeBase: current.AllowKnowledgeCreation, TenantID: req.channel.TenantID, AccountID: current.AccountID, ChannelID: req.channelID, ScopeID: current.ScopeID, ScopeName: current.ScopeName, GroupID: req.msg.Extra["octo_group_id"], SubareaID: req.msg.Extra["octo_subarea_id"], UserID: req.msg.UserID, UserName: current.SenderName, MessageID: req.msg.MessageID, MessageText: text, IsDirect: req.msg.ChatType == ChatTypeDirect, KnowledgeBaseIDs: current.KnowledgeBaseIDs, ManageKnowledgeBaseIDs: current.ManageKnowledgeBaseIDs, CanManageScope: current.CanManageScope, ReadIssueScopeIDs: current.ReadIssueScopeIDs, ReadIssueKnowledgeBaseIDs: current.ReadIssueKnowledgeBaseIDs}
		if req.msg.FileKey != "" {
			p.Attachments = []octobusiness.Attachment{{Name: req.msg.FileName, URL: req.msg.FileKey, Type: string(req.msg.MessageType)}}
		}
		return p
	}
	p := principal(scope)
	p.Validate = func(callCtx context.Context) (octobusiness.Principal, error) {
		current, err := authorizeExecution(callCtx, req.adapter, req.channel, req.msg)
		if err != nil {
			return octobusiness.Principal{}, err
		}
		return principal(current), nil
	}
	ctx = octobusiness.WithPrincipal(ctx, p)
	ctx = octobusiness.WithRetrievalTrace(ctx)
	if sender, ok := req.adapter.(generatedFileSender); ok {
		ctx = octobusiness.WithReportSink(ctx, func(callCtx context.Context, file *octobusiness.ReportArtifact) error {
			return sender.SendGeneratedFile(callCtx, req.msg, file.Name, []byte(file.Content), file.ContentType)
		})
	}
	return ctx
}

func GetStringFromChannel(channel *IMChannel, key string) string {
	config, err := ParseCredentials(channel.Credentials)
	if err != nil {
		return ""
	}
	return GetString(config, key)
}
