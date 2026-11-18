package octobusiness

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

type qualitySettings struct {
	TenantID      uint64    `json:"-" gorm:"primaryKey"`
	Enabled       bool      `json:"enabled"`
	ModelID       string    `json:"model_id"`
	PerRun        int       `json:"per_run"`
	DailyLimit    int       `json:"daily_limit"`
	SamplePercent int       `json:"sample_percent"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (qualitySettings) TableName() string { return "octo_quality_settings" }

func qualityDefaults(tenant uint64) qualitySettings {
	return qualitySettings{TenantID: tenant, PerRun: 4, DailyLimit: 24, SamplePercent: 10}
}

func (s *Service) readQualitySettings(ctx context.Context, tenant uint64) (qualitySettings, error) {
	var settings qualitySettings
	err := s.db.WithContext(ctx).Where("tenant_id = ?", tenant).Take(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return qualityDefaults(tenant), nil
	}
	return settings, err
}

func (s *Service) writeQualitySettings(
	ctx context.Context, models qualityModelResolver, settings qualitySettings,
) (qualitySettings, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return settings, ErrDenied
	}
	if settings.PerRun < 1 || settings.PerRun > 12 || settings.DailyLimit < settings.PerRun ||
		settings.DailyLimit > 500 || settings.SamplePercent < 0 || settings.SamplePercent > 100 ||
		settings.ModelID != "" && !validText(settings.ModelID, 36) {
		return settings, ErrInvalid
	}
	if settings.ModelID != "" {
		if models == nil {
			return settings, ErrInvalid
		}
		if _, err := models.GetChatModel(ctx, settings.ModelID); err != nil {
			return settings, ErrInvalid
		}
	}
	settings.TenantID, settings.UpdatedAt = p.TenantID, time.Now().UTC()
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&settings).Error
	return settings, err
}

func (s *Service) writeQualityReview(ctx context.Context, review qualityReview, columns []string) error {
	return s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"}, {Name: "channel_id"}, {Name: "scope_id"}, {Name: "message_id"},
			},
			DoUpdates: clause.AssignmentColumns(columns),
		}).Create(&review).Error
}

func qualityAdvice(category string) string {
	switch category {
	case "adequate":
		return "dismiss"
	case "uncertain":
		return "uncertain"
	case "watch":
		return "watch"
	default:
		return "process"
	}
}

func qualityFindingFields(model string, finding qualityFinding, signature string) map[string]any {
	body, _ := json.Marshal(finding)
	return map[string]any{
		"state": "done", "suggestion": qualityAdvice(finding.Category), "model_id": model,
		"finding": string(body), "evidence_signature": signature, "available_at": nil,
		"updated_at": time.Now().UTC(),
	}
}

func (s *Service) recordQualityFinding(
	ctx context.Context, review qualityReview, model string, finding qualityFinding, signature string,
) error {
	review.UpdatedAt = time.Now().UTC()
	if err := s.writeQualityReview(ctx, review, []string{"updated_at"}); err != nil {
		return err
	}
	fields := qualityFindingFields(model, finding, signature)
	fields["library_id"] = review.LibraryID
	fields["topic"] = gorm.Expr("CASE WHEN topic = '' THEN ? ELSE topic END", limitQualityText(finding.Topic, 120))
	return s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx).Model(&qualityReview{}).
		Where("tenant_id = ? AND channel_id = ? AND scope_id = ? AND message_id = ?",
			review.TenantID, review.ChannelID, review.ScopeID, review.MessageID).
		Updates(fields).Error
}

type qualityAttempt struct {
	ID        string
	TenantID  uint64
	ChannelID string
	ScopeID   string
	MessageID string
	StartedAt time.Time
}

func (qualityAttempt) TableName() string { return "octo_quality_attempts" }

func qualityWorkerContext(ctx context.Context, tenant uint64) context.Context {
	const worker = "octo-quality-worker"
	ctx = types.WithCaller(ctx, types.Caller{TenantID: tenant, UserID: worker, Role: types.TenantRoleAdmin})
	ctx = types.WithExecutionTenant(ctx, tenant)
	return WithPrincipal(ctx, Principal{TenantID: tenant, UserID: worker, Console: true, ChannelID: "console"})
}

func (s *Service) runQualityReviews(ctx context.Context, models qualityModelResolver) error {
	if models == nil {
		return nil
	}
	now := time.Now().UTC()
	q := s.db.WithContext(ctx)
	// Only the lease owner may save a result. Stale jobs can be claimed again.
	if err := q.Model(&qualityReview{}).Where("state = ? AND available_at <= ?", "working", now).
		Update("state", gorm.Expr("CASE WHEN attempt_count >= 3 THEN 'failed' ELSE 'queued' END")).Error; err != nil {
		return err
	}
	var settings []qualitySettings
	if err := q.Where("enabled = TRUE").Order("updated_at DESC").Limit(100).Find(&settings).Error; err != nil {
		return err
	}
	for _, setting := range settings {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.scheduleQualityCandidates(qualityWorkerContext(ctx, setting.TenantID), setting, now)
	}
	var pending []qualityReview
	err := q.Where("state = ? AND (available_at IS NULL OR available_at <= ?)", "queued", now).
		Order("updated_at, tenant_id, message_id").Limit(32).Find(&pending).Error
	if err != nil {
		return err
	}
	tenantRuns := map[uint64]int{}
	for _, item := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tenantCtx := qualityWorkerContext(ctx, item.TenantID)
		setting, err := s.readQualitySettings(tenantCtx, item.TenantID)
		if err != nil || tenantRuns[item.TenantID] >= setting.PerRun {
			continue
		}
		if !s.reserveQualityAttempt(tenantCtx, item, setting) {
			continue
		}
		tenantRuns[item.TenantID]++
		s.executeQualityReview(tenantCtx, models, item, setting)
	}
	return nil
}

func (s *Service) reserveQualityAttempt(ctx context.Context, item qualityReview, settings qualitySettings) bool {
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if settings.UpdatedAt.IsZero() {
			settings.UpdatedAt = now
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&settings).Error; err != nil {
				return err
			}
		}
		var lock qualitySettings
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ?", item.TenantID).Take(&lock).Error; err != nil {
			return err
		}
		var count int64
		day := now.In(time.FixedZone("Asia/Shanghai", 8*3600))
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location()).UTC()
		if err := tx.Model(&qualityAttempt{}).Where("tenant_id = ? AND started_at >= ?", item.TenantID, start).
			Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(lock.DailyLimit) {
			return ErrConflict
		}
		match := tx.Model(&qualityReview{}).Where(
			"tenant_id = ? AND channel_id = ? AND scope_id = ? AND message_id = ?",
			item.TenantID, item.ChannelID, item.ScopeID, item.MessageID,
		).Where("state = ? AND attempt_count = ?", "queued", item.AttemptCount).Updates(map[string]any{
			"state": "working", "available_at": now.Add(5 * time.Minute),
			"attempt_count": item.AttemptCount + 1, "updated_at": now,
		})
		if match.Error != nil {
			return match.Error
		}
		if match.RowsAffected != 1 {
			return ErrConflict
		}
		return tx.Create(&qualityAttempt{
			ID: uuid.NewString(), TenantID: item.TenantID, ChannelID: item.ChannelID,
			ScopeID: item.ScopeID, MessageID: item.MessageID, StartedAt: now,
		}).Error
	})
	return err == nil
}

func (s *Service) scheduleQualityCandidates(ctx context.Context, setting qualitySettings, now time.Time) {
	var scopes []octointegration.Scope
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND sync_status = ?", setting.TenantID, "verified").
		Order("id").Limit(100).Find(&scopes).Error; err != nil {
		return
	}
	type candidate struct {
		review qualityReview
		risk   int
	}
	var candidates []candidate
	for _, area := range scopes {
		if ctx.Err() != nil {
			return
		}
		scope, err := s.qualityScope(ctx, area.ID)
		if err != nil {
			continue
		}
		rows, _, err := s.collectQuality(ctx, scope, now.AddDate(0, 0, -14), now)
		if err != nil || s.enrichQuality(ctx, scope, rows) != nil {
			continue
		}
		for _, row := range rows {
			if len(row.libraries) == 0 || row.Review != nil && row.Review.State != "" {
				continue
			}
			risk := 0
			if row.Outcome != "answered" || row.UnavailableSources > 0 {
				risk = 1
			}
			bucket, _ := strconv.ParseUint(digest(row.ID)[:4], 16, 32)
			if risk == 0 && int(bucket%100) >= setting.SamplePercent {
				continue
			}
			library := row.libraries[0]
			if len(row.Sources) > 0 {
				library = row.Sources[0].Library
			}
			candidates = append(candidates, candidate{
				review: qualityReview{
					TenantID: scope.TenantID, ScopeID: scope.ID, ChannelID: scope.ChannelID,
					MessageID: row.ID, LibraryID: library, State: "queued", UpdatedAt: now,
				},
				risk: risk,
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].risk > candidates[j].risk })
	for _, item := range candidates[:min(len(candidates), setting.PerRun)] {
		_ = s.writeQualityReview(ctx, item.review, []string{"updated_at"})
		_ = s.db.WithContext(ctx).Model(&qualityReview{}).
			Where("tenant_id = ? AND channel_id = ? AND scope_id = ? AND message_id = ? AND state = ''",
				item.review.TenantID, item.review.ChannelID, item.review.ScopeID, item.review.MessageID).
			Updates(map[string]any{"state": "queued", "library_id": item.review.LibraryID}).Error
	}
}

func (s *Service) executeQualityReview(
	ctx context.Context, models qualityModelResolver, item qualityReview, setting qualitySettings,
) {
	input := qualityAnalysisInput{ScopeID: item.ScopeID, LibraryID: item.LibraryID, Messages: []string{item.MessageID}}
	scope, err := s.qualityScope(ctx, item.ScopeID)
	var fields map[string]any
	if err == nil && scope.ChannelID == item.ChannelID {
		fields, err = s.runQualityFinding(ctx, models, scope, input, setting)
	}
	if err != nil || fields == nil {
		fields = map[string]any{"state": "failed", "available_at": nil, "updated_at": time.Now().UTC()}
		if item.AttemptCount < 2 && errors.Is(err, errQualityAnalysis) {
			backoff := time.Duration(item.AttemptCount+1) * time.Minute
			fields["state"], fields["available_at"] = "queued", time.Now().UTC().Add(backoff)
		}
	}
	// A shutdown may cancel the model request; persist the outcome before leaving.
	write, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	_ = s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(write).Model(&qualityReview{}).
		Where("tenant_id = ? AND channel_id = ? AND scope_id = ? AND message_id = ?",
			item.TenantID, item.ChannelID, item.ScopeID, item.MessageID).
		Where("state = 'working' AND attempt_count = ?", item.AttemptCount+1).Updates(fields).Error
}

func (s *Service) runQualityFinding(
	ctx context.Context, models qualityModelResolver, scope *qualityScope,
	input qualityAnalysisInput, settings qualitySettings,
) (map[string]any, error) {
	rows, err := s.loadQualityRows(ctx, scope, input)
	if err != nil {
		return nil, err
	}
	excerpts, err := s.qualityMaterials(ctx, scope, input.LibraryID, rows)
	if err != nil {
		return nil, err
	}
	model := settings.ModelID
	if model == "" {
		model = scope.ModelID
	}
	finding, err := assessQuality(ctx, models, model, excerpts)
	if err != nil {
		return nil, err
	}
	fresh, err := s.qualityScope(ctx, input.ScopeID)
	if err != nil || fresh.ChannelID != scope.ChannelID {
		return nil, ErrDenied
	}
	freshRows, err := s.loadQualityRows(ctx, fresh, input)
	if err != nil {
		return nil, err
	}
	current, err := s.qualityMaterials(ctx, fresh, input.LibraryID, freshRows)
	signature := qualitySignature(excerpts)
	if err != nil || signature != qualitySignature(current) {
		return nil, ErrConflict
	}
	fields := qualityFindingFields(model, finding, signature)
	fields["topic"] = gorm.Expr("CASE WHEN topic = '' THEN ? ELSE topic END", limitQualityText(finding.Topic, 120))
	return fields, nil
}
