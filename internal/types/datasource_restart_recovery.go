package types

import (
	"time"

	"gorm.io/gorm"
)

const (
	// RestartInterruptedKnowledgeError is written only by the startup cleanup
	// path when a non-durable Lite worker is lost. Recovery intentionally does
	// not retry arbitrary failed imports: only this exact terminal marker is
	// eligible for the local-file replay contract.
	RestartInterruptedKnowledgeError = "Task interrupted due to application restart"

	DataSourceRestartRecoveryStatusPending   = "pending"
	DataSourceRestartRecoveryStatusRunning   = "running"
	DataSourceRestartRecoveryStatusCompleted = "completed"
	DataSourceRestartRecoveryStatusPartial   = "partial"
	DataSourceRestartRecoveryStatusFailed    = "failed"
	DataSourceRestartRecoveryStatusBlocked   = "blocked"

	DataSourceRestartRecoveryCandidatePending   = "pending"
	DataSourceRestartRecoveryCandidateReparsing = "reparsing"
	DataSourceRestartRecoveryCandidatePublished = "published"
	DataSourceRestartRecoveryCandidateFailed    = "failed"
	DataSourceRestartRecoveryCandidateBlocked   = "blocked"
	DataSourceRestartRecoveryCandidateExcluded  = "excluded"
)

// DataSourceRestartRecoveryCandidate is an auditable, display-safe snapshot
// of one interrupted repository-document candidate. It intentionally omits
// FilePath and raw metadata: those can reveal storage topology or connector
// internals and are re-read by the worker from the database instead.
type DataSourceRestartRecoveryCandidate struct {
	KnowledgeID      string `json:"knowledge_id"`
	Title            string `json:"title,omitempty"`
	FileName         string `json:"file_name,omitempty"`
	SourceVersion    string `json:"source_version,omitempty"`
	TargetExternalID string `json:"target_external_id,omitempty"`
	Fingerprint      string `json:"fingerprint"`
	State            string `json:"state"`
	Reason           string `json:"reason,omitempty"`
	AttemptedAt      string `json:"attempted_at,omitempty"`
	CompletedAt      string `json:"completed_at,omitempty"`
}

// DataSourceRestartRecoveryPlan is persisted in a recovery run. Its digest is
// verified before a run is created; each candidate has its own fingerprint so
// a process restart can resume already-started work without treating normal
// parser state changes as a stale preview.
type DataSourceRestartRecoveryPlan struct {
	Version                  int    `json:"version"`
	DataSourceFingerprint    string `json:"data_source_fingerprint"`
	KnowledgeBaseFingerprint string `json:"knowledge_base_fingerprint"`
	// Candidates is the executable, signed set only. Excluded and blocked
	// preview rows intentionally never enter the persisted plan or digest, so
	// a later worker cannot accidentally act on an unreadable/unsafe file.
	Candidates []DataSourceRestartRecoveryCandidate `json:"candidates"`
	Blockers   []string                             `json:"blockers,omitempty"`
	// PreviewCandidates is deliberately transient. It lets the UI explain why
	// a row was excluded without granting it execution authority or persisting
	// private storage details inside an approved recovery run.
	PreviewCandidates []DataSourceRestartRecoveryCandidate `json:"-"`
}

// DataSourceRestartRecoveryPreview is read-only. PreviewToken binds the exact
// plan digest to the requesting principal and expires before execution; it
// never authorizes a different source, tenant, or changed candidate set.
type DataSourceRestartRecoveryPreview struct {
	DataSourceID    string                               `json:"data_source_id"`
	KnowledgeBaseID string                               `json:"knowledge_base_id"`
	EligibleCount   int                                  `json:"eligible_count"`
	ExcludedCount   int                                  `json:"excluded_count"`
	BlockedCount    int                                  `json:"blocked_count"`
	Candidates      []DataSourceRestartRecoveryCandidate `json:"candidates"`
	Blockers        []string                             `json:"blockers,omitempty"`
	PlanDigest      string                               `json:"plan_digest"`
	PreviewToken    string                               `json:"preview_token"`
	ExpiresAt       string                               `json:"expires_at"`
}

// DataSourceRestartRecoveryRequest is intentionally separate from ordinary
// sync controls. The signed preview token carries the digest/CAS contract;
// callers cannot submit arbitrary knowledge IDs or source paths.
type DataSourceRestartRecoveryRequest struct {
	PreviewToken string `json:"preview_token"`
}

// DataSourceRestartRecoveryRun is the durable state machine behind recovery.
// The task trigger may be ephemeral in Lite mode, but this row survives a
// restart and lets startup safely re-arm only an already-approved plan.
type DataSourceRestartRecoveryRun struct {
	ID              string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64     `json:"tenant_id" gorm:"index"`
	DataSourceID    string     `json:"data_source_id" gorm:"index"`
	KnowledgeBaseID string     `json:"knowledge_base_id" gorm:"index"`
	PlanDigest      string     `json:"plan_digest" gorm:"type:varchar(64)"`
	Plan            JSON       `json:"plan" gorm:"type:jsonb"`
	Status          string     `json:"status" gorm:"type:varchar(32);index"`
	LeaseID         string     `json:"-" gorm:"type:varchar(36)"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (DataSourceRestartRecoveryRun) TableName() string {
	return "datasource_restart_recovery_runs"
}

func (r *DataSourceRestartRecoveryRun) BeforeCreate(_ *gorm.DB) error {
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	return nil
}

// DataSourceRestartRecoveryPayload is the only data carried by the queued
// task. The worker re-reads the durable run and all source records; it never
// contains a provider URL, GitHub token, source bytes, or FilePath.
type DataSourceRestartRecoveryPayload struct {
	TenantID     uint64 `json:"tenant_id"`
	DataSourceID string `json:"data_source_id"`
	RunID        string `json:"run_id"`
}
