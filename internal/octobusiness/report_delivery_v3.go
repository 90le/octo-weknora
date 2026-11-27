package octobusiness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DeliveryTarget represents one Octo destination (a joined group/subarea,
// bound or unbound, or a verified friend DM) for multi-target report delivery.
type DeliveryTarget struct {
	Key          string `json:"key"`          // group:<group_id>[:<subarea_id>] | dm:<uid>
	Kind         string `json:"kind"`         // group | dm
	GroupID      string `json:"group_id"`     // populated for kind=group
	SubareaID    string `json:"subarea_id"`   // optional subarea
	UID          string `json:"uid"`          // populated for kind=dm
	Label        string `json:"label"`        // human display name
	ChannelKind  string `json:"channel_kind"` // 主群 | 子区 | 私聊
	BoundKBCount int    `json:"bound_kb_count"`
	Inherited    bool   `json:"inherited"`
}

// ReportRunDelivery records per-target delivery receipts and failure/uncertain
// isolation for a ReportRun so already-sent or uncertain targets are never resent.
type ReportRunDelivery struct {
	ID             string     `json:"id" gorm:"primaryKey"`
	TenantID       uint64     `json:"-"`
	RunID          string     `json:"run_id"`
	ScheduleID     string     `json:"schedule_id"`
	SenderChannel  string     `json:"sender_channel"`
	TargetKey      string     `json:"target_key"`
	TargetKind     string     `json:"target_kind"`
	TargetLabel    string     `json:"target_label"`
	Status         string     `json:"status"` // pending | sending | sent | failed | uncertain
	Receipt        string     `json:"receipt"`
	ErrorCode      string     `json:"error_code"`
	ErrorMessage   string     `json:"error_message"`
	IdempotencyKey string     `json:"-" gorm:"uniqueIndex"`
	AttemptCount   int        `json:"attempt_count"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (ReportRunDelivery) TableName() string { return "octo_report_run_deliveries" }

type BotDeliveryGroup struct {
	Key          string `json:"key"`
	GroupID      string `json:"group_id"`
	SubareaID    string `json:"subarea_id"`
	Kind         string `json:"kind"` // 主群 | 子区
	Label        string `json:"label"`
	BoundKBCount int    `json:"bound_kb_count"`
	Inherited    bool   `json:"inherited"`
}

type BotDMCandidate struct {
	UID    string `json:"uid"`
	Name   string `json:"name"`
	Source string `json:"source"` // recent_dm | contact | creator
}

type BotDeliveryTargetsResponse struct {
	ChannelID    string             `json:"channel_id"`
	AccountID    string             `json:"account_id"`
	BotName      string             `json:"bot_name"`
	Groups       []BotDeliveryGroup `json:"groups"`
	DMCandidates []BotDMCandidate   `json:"dm_candidates"`
}

type BotFriendVerifyResult struct {
	OK            bool     `json:"ok"`
	UID           string   `json:"uid"`
	Name          string   `json:"name"`
	IsCreator     bool     `json:"is_creator"`
	ReasonCode    string   `json:"reason_code,omitempty"`
	ReasonMessage string   `json:"reason_message,omitempty"`
	SuggestedBots []string `json:"suggested_bots,omitempty"`
}

type ReportRunViewV3 struct {
	ReportRun
	SenderChannelID string              `json:"sender_channel_id"`
	ScheduleName    string              `json:"schedule_name"`
	Template        string              `json:"template"`
	Deliveries      []ReportRunDelivery `json:"deliveries"`
}

func hydrateScheduleTargets(s *ReportSchedule) {
	if s == nil {
		return
	}
	if s.SenderChannelID == "" {
		s.SenderChannelID = s.ChannelID
	}
	if strings.TrimSpace(s.DeliveryTargetsJSON) != "" && s.DeliveryTargetsJSON != "[]" {
		var parsed []DeliveryTarget
		if json.Unmarshal([]byte(s.DeliveryTargetsJSON), &parsed) == nil {
			s.DeliveryTargets = parsed
			return
		}
	}
	s.DeliveryTargets = []DeliveryTarget{}
}

func serializeScheduleTargets(s *ReportSchedule) error {
	if s == nil {
		return nil
	}
	if s.SenderChannelID == "" {
		s.SenderChannelID = s.ChannelID
	}
	if s.ChannelID == "" {
		s.ChannelID = s.SenderChannelID
	}
	if len(s.DeliveryTargets) == 0 {
		if strings.TrimSpace(s.DeliveryTargetsJSON) == "" {
			s.DeliveryTargetsJSON = "[]"
		}
		return nil
	}
	if len(s.DeliveryTargets) > 30 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	clean := make([]DeliveryTarget, 0, len(s.DeliveryTargets))
	for _, t := range s.DeliveryTargets {
		if t.Kind != "group" && t.Kind != "dm" {
			return ErrInvalid
		}
		if t.Kind == "group" && !validText(t.GroupID, 128) {
			return ErrInvalid
		}
		if t.Kind == "dm" && !validText(t.UID, 128) {
			return ErrInvalid
		}
		if t.Key == "" {
			if t.Kind == "dm" {
				t.Key = "dm:" + t.UID
			} else if t.SubareaID != "" {
				t.Key = "group:" + t.GroupID + ":" + t.SubareaID
			} else {
				t.Key = "group:" + t.GroupID
			}
		}
		if seen[t.Key] {
			continue
		}
		seen[t.Key] = true
		clean = append(clean, t)
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	s.DeliveryTargets = clean
	s.DeliveryTargetsJSON = string(raw)
	return nil
}

// BotDeliveryTargets returns all groups/subareas joined by the selected Octo
// Bot (including unbound management groups) plus system-known DM candidates.
func (h *Handler) BotDeliveryTargets(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	p, _ := currentPrincipal(ctx)
	channelID := c.Param("channelId")
	if !validText(channelID, 128) {
		respond(c, nil, ErrInvalid)
		return
	}
	var out *BotDeliveryTargetsResponse
	if h.service.BotDeliveryTargets != nil {
		out, err = h.service.BotDeliveryTargets(ctx, p.TenantID, channelID)
	} else {
		out, err = h.service.fallbackBotDeliveryTargets(ctx, p.TenantID, channelID)
	}
	if err == nil && out != nil {
		out.DMCandidates = h.service.collectDMCandidates(ctx, p.TenantID, channelID, out.DMCandidates)
	}
	respond(c, out, err)
}

// VerifyBotFriend checks live via Octo CheckDMFriend whether the given UID is
// a friend (or creator) of the selected Bot, and suggests another Bot if found.
func (h *Handler) VerifyBotFriend(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	p, _ := currentPrincipal(ctx)
	channelID := c.Param("channelId")
	var in struct {
		UID string `json:"uid"`
	}
	if !validText(channelID, 128) || c.ShouldBindJSON(&in) != nil || !validText(in.UID, 128) {
		respond(c, nil, ErrInvalid)
		return
	}
	if h.service.BotVerifyFriend == nil {
		respond(c, &BotFriendVerifyResult{
			OK:            false,
			UID:           strings.TrimSpace(in.UID),
			ReasonCode:    "adapter_unavailable",
			ReasonMessage: "当前未连接实时 Octo 适配器，无法校验好友关系",
		}, nil)
		return
	}
	res, err := h.service.BotVerifyFriend(ctx, p.TenantID, channelID, strings.TrimSpace(in.UID))
	respond(c, res, err)
}

func (s *Service) fallbackBotDeliveryTargets(ctx context.Context, tenant uint64, channelID string) (*BotDeliveryTargetsResponse, error) {
	quiet := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	var ch struct {
		ID, Name, Credentials string
	}
	if err := quiet.Table("im_channels").Select("id, name, credentials").
		Where("tenant_id = ? AND id = ? AND platform = 'octo' AND deleted_at IS NULL", tenant, channelID).
		Take(&ch).Error; err != nil {
		return nil, err
	}
	var creds map[string]any
	_ = json.Unmarshal([]byte(ch.Credentials), &creds)
	accountID, _ := creds["account_id"].(string)

	var scopes []octointegration.Scope
	_ = quiet.Where("tenant_id = ? AND account_id = ?", tenant, accountID).Order("group_id, subarea_id").Find(&scopes).Error
	groups := make([]BotDeliveryGroup, 0, len(scopes))
	store := octointegration.NewStore(s.db)
	for _, sc := range scopes {
		eff, _ := store.Effective(ctx, tenant, sc.ID)
		kind := "主群"
		key := "group:" + sc.GroupID
		if sc.SubareaID != "" {
			kind = "子区"
			key += ":" + sc.SubareaID
		}
		groups = append(groups, BotDeliveryGroup{
			Key:          key,
			GroupID:      sc.GroupID,
			SubareaID:    sc.SubareaID,
			Kind:         kind,
			Label:        sc.DisplayName,
			BoundKBCount: len(eff),
			Inherited:    sc.InheritParent && sc.SubareaID != "",
		})
	}
	return &BotDeliveryTargetsResponse{
		ChannelID:    ch.ID,
		AccountID:    accountID,
		BotName:      ch.Name,
		Groups:       groups,
		DMCandidates: []BotDMCandidate{},
	}, nil
}

func (s *Service) collectDMCandidates(ctx context.Context, tenant uint64, channelID string, existing []BotDMCandidate) []BotDMCandidate {
	quiet := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	seen := map[string]bool{}
	out := make([]BotDMCandidate, 0, len(existing)+12)
	for _, c := range existing {
		if c.UID != "" && !seen[c.UID] {
			seen[c.UID] = true
			out = append(out, c)
		}
	}
	// 1. Recent private DM senders seen by this Bot channel (capped at 15 so contacts are never starved)
	var inbox []struct{ Input string }
	recentAdded := 0
	if quiet.Table("octo_message_inbox").Select("input").
		Where("tenant_id = ? AND channel_id = ?", tenant, channelID).
		Order("created_at DESC").Limit(120).Find(&inbox).Error == nil {
		for _, row := range inbox {
			var in struct {
				UserID, UserName, ChatType string
			}
			if json.Unmarshal([]byte(row.Input), &in) == nil && in.ChatType == "direct" && in.UserID != "" && !seen[in.UserID] {
				seen[in.UserID] = true
				out = append(out, BotDMCandidate{
					UID:    in.UserID,
					Name:   firstNonEmpty(in.UserName, in.UserID),
					Source: "recent_dm",
				})
				recentAdded++
				if recentAdded >= 15 {
					break
				}
			}
		}
	}
	// 2. Configured KB contacts in this tenant
	var contacts []Contact
	if quiet.Where("tenant_id = ?", tenant).Order("is_default DESC, name").Limit(50).Find(&contacts).Error == nil {
		for _, c := range contacts {
			if c.UID != "" {
				if seen[c.UID] {
					// Upgrade existing raw-UID name with configured contact name when available.
					for i := range out {
						if out[i].UID == c.UID && (out[i].Name == "" || out[i].Name == out[i].UID) && strings.TrimSpace(c.Name) != "" {
							out[i].Name = strings.TrimSpace(c.Name)
						}
					}
					continue
				}
				seen[c.UID] = true
				out = append(out, BotDMCandidate{
					UID:    c.UID,
					Name:   firstNonEmpty(c.Name, c.UID),
					Source: "contact",
				})
				if len(out) >= 40 {
					break
				}
			}
		}
	}
	// 3. Batch-resolve Octo nicknames for any candidate whose Name is still empty or raw UID
	missing := make([]string, 0, len(out))
	for _, c := range out {
		if c.UID != "" && (strings.TrimSpace(c.Name) == "" || c.Name == c.UID) {
			missing = append(missing, c.UID)
		}
	}
	if len(missing) > 0 {
		resolved := s.resolveOctoNicknames(ctx, tenant, channelID, missing)
		for i := range out {
			if nick := resolved[out[i].UID]; nick != "" && (strings.TrimSpace(out[i].Name) == "" || out[i].Name == out[i].UID) {
				out[i].Name = nick
			}
		}
	}
	return out
}

// CreateAdhocReportRun queues an on-demand report snapshot with multi-target
// Octo Bot recipients for manual approval without creating a schedule.
func (h *Handler) CreateAdhocReportRun(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	p, _ := currentPrincipal(ctx)
	var in struct {
		Title           string           `json:"title"`
		SenderChannelID string           `json:"sender_channel_id"`
		DeliveryTargets []DeliveryTarget `json:"delivery_targets"`
		Format          string           `json:"format"`
		CustomNote      string           `json:"custom_note"`
		ScopeID         string           `json:"scope_id"`
		KnowledgeBaseID string           `json:"knowledge_base_id"`
		From            *time.Time       `json:"from"`
		To              *time.Time       `json:"to"`
	}
	if c.ShouldBindJSON(&in) != nil || !validText(in.SenderChannelID, 128) || len(in.DeliveryTargets) == 0 || len(in.CustomNote) > 2000 {
		respond(c, nil, ErrInvalid)
		return
	}
	tmp := ReportSchedule{SenderChannelID: in.SenderChannelID, DeliveryTargets: in.DeliveryTargets}
	if err := serializeScheduleTargets(&tmp); err != nil {
		respond(c, nil, err)
		return
	}
	now := time.Now().UTC()
	to := now
	from := now.AddDate(0, 0, -14)
	if in.From != nil {
		from = in.From.UTC()
	}
	if in.To != nil {
		to = in.To.UTC()
	}
	report, err := h.service.Report(ctx, IssueFilter{
		ScopeID:         in.ScopeID,
		KnowledgeBaseID: in.KnowledgeBaseID,
		From:            &from,
		To:              &to,
	})
	if err != nil {
		respond(c, nil, err)
		return
	}
	if strings.TrimSpace(in.CustomNote) != "" {
		report.Notice = strings.TrimSpace(in.CustomNote)
	}
	snap, _ := json.Marshal(report)
	labels := make([]string, 0, len(tmp.DeliveryTargets))
	for _, t := range tmp.DeliveryTargets {
		labels = append(labels, firstNonEmpty(t.Label, t.Key))
	}
	runID := uuid.NewString()
	run := ReportRun{
		ID:         runID,
		TenantID:   p.TenantID,
		ScheduleID: "adhoc",
		DueAt:      now,
		Status:     "pending",
		FromAt:     &from,
		ToAt:       &to,
		Target:     limitQualityText(strings.Join(labels, "、"), 240),
		Snapshot:   string(snap),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	deliveries := make([]ReportRunDelivery, 0, len(tmp.DeliveryTargets))
	for _, t := range tmp.DeliveryTargets {
		deliveries = append(deliveries, ReportRunDelivery{
			ID:             uuid.NewString(),
			TenantID:       p.TenantID,
			RunID:          runID,
			ScheduleID:     "adhoc",
			SenderChannel:  in.SenderChannelID,
			TargetKey:      t.Key,
			TargetKind:     t.Kind,
			TargetLabel:    firstNonEmpty(t.Label, t.Key),
			Status:         "pending",
			IdempotencyKey: digest("report_delivery", fmtTenant(p.TenantID), runID, t.Key, "0"),
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}
	err = h.service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		for i := range deliveries {
			if err := tx.Create(&deliveries[i]).Error; err != nil {
				return err
			}
		}
		return audit(tx, p, "octo.report.adhoc_queued", runID, map[string]any{
			"sender_channel_id": in.SenderChannelID,
			"target_count":      len(deliveries),
		})
	})
	if err != nil {
		respond(c, nil, err)
		return
	}
	if file, fErr := ReportFile(report, "markdown"); fErr == nil {
		run.Summary = file.Content
	}
	respond(c, ReportRunViewV3{
		ReportRun:       run,
		SenderChannelID: in.SenderChannelID,
		ScheduleName:    firstNonEmpty(strings.TrimSpace(in.Title), "按需汇总报告"),
		Template:        "quality",
		Deliveries:      deliveries,
	}, nil)
}

// ListAllReportRuns lists tenant-wide report runs (scheduled + ad-hoc) with
// per-target delivery receipts attached.
func (h *Handler) ListAllReportRuns(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	p, _ := currentPrincipal(ctx)
	status := c.Query("status")
	q := h.service.db.WithContext(ctx).Omit("snapshot").Where("tenant_id = ?", p.TenantID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var runs []ReportRun
	if err := q.Order("due_at DESC, id DESC").Limit(100).Find(&runs).Error; err != nil {
		respond(c, nil, err)
		return
	}
	schedMap := map[string]ReportSchedule{}
	var scheds []ReportSchedule
	_ = h.service.db.WithContext(ctx).Where("tenant_id = ?", p.TenantID).Find(&scheds).Error
	for _, sc := range scheds {
		hydrateScheduleTargets(&sc)
		schedMap[sc.ID] = sc
	}
	runIDs := make([]string, 0, len(runs))
	for _, r := range runs {
		runIDs = append(runIDs, r.ID)
	}
	delivByRun := map[string][]ReportRunDelivery{}
	if len(runIDs) > 0 {
		var rows []ReportRunDelivery
		if h.service.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx).
			Where("tenant_id = ? AND run_id IN ?", p.TenantID, runIDs).
			Order("created_at ASC, id ASC").Find(&rows).Error == nil {
			for _, d := range rows {
				delivByRun[d.RunID] = append(delivByRun[d.RunID], d)
			}
		}
	}
	out := make([]ReportRunViewV3, 0, len(runs))
	for _, r := range runs {
		sc := schedMap[r.ScheduleID]
		name := sc.Name
		if name == "" {
			if r.ScheduleID == "adhoc" {
				name = "按需汇总报告"
			} else {
				name = "历史报告"
			}
		}
		sender := sc.SenderChannelID
		ds := delivByRun[r.ID]
		if sender == "" && len(ds) > 0 {
			sender = ds[0].SenderChannel
		}
		if ds == nil {
			ds = []ReportRunDelivery{}
		}
		out = append(out, ReportRunViewV3{
			ReportRun:       r,
			SenderChannelID: sender,
			ScheduleName:    name,
			Template:        sc.Template,
			Deliveries:      ds,
		})
	}
	respond(c, out, nil)
}

// SendReportRunV3 approves and delivers a pending ad-hoc or multi-target run
// target-by-target, isolating sent / failed / uncertain outcomes per target.
func (h *Handler) SendReportRunV3(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	p, _ := currentPrincipal(ctx)
	runID := c.Param("run")
	out, err := h.service.deliverMultiTargetRun(ctx, p, runID, "")
	respond(c, out, err)
}

// RetryReportDelivery strictly retries ONE failed target delivery in a run,
// refusing to resend already-sent or uncertain deliveries.
func (h *Handler) RetryReportDelivery(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	p, _ := currentPrincipal(ctx)
	out, err := h.service.deliverMultiTargetRun(ctx, p, c.Param("run"), c.Param("deliveryId"))
	respond(c, out, err)
}

func (s *Service) deliverMultiTargetRun(ctx context.Context, p Principal, runID, onlyDeliveryID string) (*ReportRunViewV3, error) {
	var run ReportRun
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", p.TenantID, runID).Take(&run).Error; err != nil {
		return nil, err
	}
	var deliveries []ReportRunDelivery
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND run_id = ?", p.TenantID, runID).
		Order("created_at ASC, id ASC").Find(&deliveries).Error; err != nil {
		return nil, err
	}
	if len(deliveries) == 0 {
		return nil, ErrConflict
	}
	var report Report
	if run.Snapshot == "" || json.Unmarshal([]byte(run.Snapshot), &report) != nil {
		return nil, ErrConflict
	}
	if s.MultiTargetDelivery == nil {
		return nil, ReportAccessError("channel")
	}

	foundTarget := onlyDeliveryID == ""
	for i := range deliveries {
		d := &deliveries[i]
		if onlyDeliveryID != "" {
			if d.ID != onlyDeliveryID {
				continue
			}
			foundTarget = true
			if d.Status != "failed" {
				// Never resend already-sent or uncertain deliveries!
				return nil, ErrConflict
			}
		} else if d.Status != "pending" {
			continue
		}

		target := deliveryTargetFromRow(*d)
		now := time.Now().UTC()
		attempt := d.AttemptCount + 1
		runKey := digest("multi_report", fmtTenant(p.TenantID), run.ID, d.TargetKey, string(rune('0'+attempt)))
		receipt, sendErr := s.MultiTargetDelivery(ctx, p.TenantID, d.SenderChannel, target, runKey, &report, nil)
		if sendErr == nil {
			d.Status = "sent"
			d.Receipt = receipt
			d.ErrorCode = ""
			d.ErrorMessage = ""
			d.AttemptCount = attempt
			d.SentAt = &now
			d.UpdatedAt = now
		} else {
			code := reportErrorCode(sendErr)
			if code == "delivery_unconfirmed" {
				d.Status = "uncertain"
				d.ErrorCode = code
				d.ErrorMessage = "平台送达未获确认，严禁自动重发，请先核对原会话"
			} else {
				d.Status = "failed"
				d.ErrorCode = code
				d.ErrorMessage = sendErr.Error()
			}
			d.AttemptCount = attempt
			d.UpdatedAt = now
		}
		_ = s.db.WithContext(ctx).Model(&ReportRunDelivery{}).Where("tenant_id = ? AND id = ?", p.TenantID, d.ID).
			Updates(map[string]any{
				"status": d.Status, "receipt": d.Receipt, "error_code": d.ErrorCode,
				"error_message": d.ErrorMessage, "attempt_count": d.AttemptCount,
				"sent_at": d.SentAt, "updated_at": d.UpdatedAt,
			}).Error
	}
	if !foundTarget {
		return nil, gorm.ErrRecordNotFound
	}

	// Recompute aggregate run status: uncertain > failed > pending > sent
	agg := "sent"
	for _, d := range deliveries {
		if d.Status == "uncertain" {
			agg = "uncertain"
			break
		}
		if d.Status == "failed" {
			agg = "failed"
		} else if d.Status == "pending" && agg == "sent" {
			agg = "pending"
		}
	}
	run.Status = agg
	run.UpdatedAt = time.Now().UTC()
	_ = s.db.WithContext(ctx).Model(&ReportRun{}).Where("tenant_id = ? AND id = ?", p.TenantID, run.ID).
		Updates(map[string]any{"status": run.Status, "updated_at": run.UpdatedAt}).Error
	_ = audit(s.db.WithContext(ctx), p, "octo.report.multi_target_delivered", run.ID, map[string]string{
		"status":           agg,
		"only_delivery_id": onlyDeliveryID,
	})

	if file, fErr := ReportFile(&report, "markdown"); fErr == nil {
		run.Summary = file.Content
	}
	return &ReportRunViewV3{
		ReportRun:       run,
		SenderChannelID: deliveries[0].SenderChannel,
		ScheduleName:    "多目标报告发送",
		Deliveries:      deliveries,
	}, nil
}

func deliveryTargetFromRow(d ReportRunDelivery) DeliveryTarget {
	t := DeliveryTarget{
		Key:   d.TargetKey,
		Kind:  d.TargetKind,
		Label: d.TargetLabel,
	}
	if d.TargetKind == "dm" {
		t.UID = strings.TrimPrefix(d.TargetKey, "dm:")
		t.ChannelKind = "私聊"
		return t
	}
	rest := strings.TrimPrefix(d.TargetKey, "group:")
	if g, sub, ok := strings.Cut(rest, ":"); ok {
		t.GroupID, t.SubareaID, t.ChannelKind = g, sub, "子区"
	} else {
		t.GroupID, t.ChannelKind = rest, "主群"
	}
	return t
}

var _ = errors.Is
