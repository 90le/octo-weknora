package octobusiness

import (
	"context"
	"strings"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func (s *Service) resolveOctoNicknames(ctx context.Context, tenant uint64, channelID string, uids []string) map[string]string {
	if s.ResolveOctoUserNames == nil || len(uids) == 0 {
		return nil
	}
	return s.ResolveOctoUserNames(ctx, tenant, channelID, uids)
}

func (s *Service) resolveConsoleUserNames(ctx context.Context, userIDs []string) map[string]string {
	out := map[string]string{}
	if s.db == nil || len(userIDs) == 0 {
		return out
	}
	uniq := make([]string, 0, len(userIDs))
	seen := map[string]bool{}
	for _, id := range userIDs {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return out
	}
	var rows []struct {
		ID       string
		Username string
		Email    string
	}
	quiet := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	if err := quiet.Table("users").Select("id, username, email").Where("id IN ?", uniq).Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		name := strings.TrimSpace(r.Username)
		if name == "" {
			name = strings.TrimSpace(r.Email)
		}
		if name != "" {
			out[r.ID] = name
		}
	}
	return out
}

func (s *Service) resolveConsoleUserName(ctx context.Context, userID, fallback string) string {
	if userID == "" {
		return fallback
	}
	if names := s.resolveConsoleUserNames(ctx, []string{userID}); names[userID] != "" {
		return names[userID]
	}
	if fallback != "" {
		return fallback
	}
	return userID
}

func (s *Service) enrichIssuesNames(ctx context.Context, tenant uint64, issues []Issue) {
	if len(issues) == 0 {
		return
	}
	byChannel := map[string][]string{}
	for _, iss := range issues {
		if iss.ReporterUID != "" && (iss.ReporterName == "" || iss.ReporterName == iss.ReporterUID) {
			byChannel[iss.ChannelID] = append(byChannel[iss.ChannelID], iss.ReporterUID)
		}
	}
	if len(byChannel) == 0 {
		return
	}
	resolved := map[string]string{}
	for chID, uids := range byChannel {
		for uid, nick := range s.resolveOctoNicknames(ctx, tenant, chID, uids) {
			if nick != "" {
				resolved[uid] = nick
			}
		}
	}
	if len(resolved) == 0 {
		return
	}
	for i := range issues {
		if nick := resolved[issues[i].ReporterUID]; nick != "" && (issues[i].ReporterName == "" || issues[i].ReporterName == issues[i].ReporterUID) {
			issues[i].ReporterName = nick
		}
	}
}

func (s *Service) enrichIssueEventsNames(ctx context.Context, tenant uint64, channelID string, events []IssueEvent) {
	if len(events) == 0 {
		return
	}
	consoleIDs := make([]string, 0, len(events))
	octoIDs := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.ActorUID != "" && (ev.ActorName == "" || ev.ActorName == ev.ActorUID) {
			consoleIDs = append(consoleIDs, ev.ActorUID)
			octoIDs = append(octoIDs, ev.ActorUID)
		}
	}
	if len(consoleIDs) == 0 {
		return
	}
	consoleNames := s.resolveConsoleUserNames(ctx, consoleIDs)
	missingForOcto := make([]string, 0, len(octoIDs))
	for _, id := range octoIDs {
		if consoleNames[id] == "" {
			missingForOcto = append(missingForOcto, id)
		}
	}
	octoNames := s.resolveOctoNicknames(ctx, tenant, channelID, missingForOcto)
	for i := range events {
		uid := events[i].ActorUID
		if uid == "" || (events[i].ActorName != "" && events[i].ActorName != uid) {
			continue
		}
		if name := consoleNames[uid]; name != "" {
			events[i].ActorName = name
		} else if nick := octoNames[uid]; nick != "" {
			events[i].ActorName = nick
		}
	}
}
