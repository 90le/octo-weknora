package types

import "time"

const (
	GitHubDocumentRunRunning     = "running"
	GitHubDocumentRunComplete    = "complete"
	GitHubDocumentRunSuperseded  = "superseded"
	GitHubDocumentItemPending    = "pending"
	GitHubDocumentItemReady      = "ready"
	GitHubDocumentItemFailed     = "failed"
	GitHubDocumentItemUpsert     = "upsert"
	GitHubDocumentItemDelete     = "delete"
	GitHubDocumentOutcomeCreated = "created"
	GitHubDocumentOutcomeUpdated = "updated"
	GitHubDocumentOutcomeSkipped = "skipped"
	GitHubDocumentOutcomeDeleted = "deleted"
)

// GitHubDocumentRun records only a fixed-commit plan and its outcome. The
// authoritative published manifest remains DataSource.LastSyncCursor until
// every planned item is acknowledged. No source bytes or credentials live here.
type GitHubDocumentRun struct {
	ID              string `gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `gorm:"not null;index"`
	KnowledgeBaseID string `gorm:"type:varchar(36);not null;index"`
	DataSourceID    string `gorm:"type:varchar(36);not null;index"`
	Selection       string `gorm:"type:varchar(64);not null"`
	CommitSHA       string `gorm:"type:varchar(40);not null"`
	PlanDigest      string `gorm:"type:varchar(64);not null"`
	CredentialScope string `gorm:"type:varchar(64);not null"`
	ForceFull       bool   `gorm:"not null;default:false"`
	Status          string `gorm:"type:varchar(24);not null;index"`
	LeaseID         string `gorm:"type:varchar(36);not null;default:''"`
	LeaseUntil      *time.Time
	CreatedAt       time.Time `gorm:"not null"`
	UpdatedAt       time.Time `gorm:"not null"`
}

func (GitHubDocumentRun) TableName() string { return "github_document_sync_runs" }

type GitHubDocumentSyncItem struct {
	RunID     string    `gorm:"type:varchar(36);primaryKey"`
	PathHash  string    `gorm:"type:varchar(64);primaryKey"`
	Path      string    `gorm:"type:text;not null"`
	BlobSHA   string    `gorm:"type:varchar(40)"`
	Operation string    `gorm:"type:varchar(8);not null"`
	Status    string    `gorm:"type:varchar(16);not null;index"`
	Outcome   string    `gorm:"type:varchar(16)"`
	ErrorCode string    `gorm:"type:varchar(64)"`
	Attempts  int       `gorm:"not null;default:0"`
	UpdatedAt time.Time `gorm:"not null"`
}

func (GitHubDocumentSyncItem) TableName() string { return "github_document_sync_items" }
