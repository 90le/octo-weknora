package types

import "time"

// GitHubRepositoryCandidate is repository metadata returned by the native
// discovery endpoint and accepted by the explicit batch-create request.
// It never contains clone URLs, tokens, or user-supplied arbitrary settings.
type GitHubRepositoryCandidate struct {
	Repository    string    `json:"repository"`
	DefaultBranch string    `json:"default_branch"`
	Description   string    `json:"description,omitempty"`
	Archived      bool      `json:"archived"`
	Disabled      bool      `json:"disabled"`
	Fork          bool      `json:"fork"`
	SizeKiB       int       `json:"size_kib"`
	UpdatedAt     time.Time `json:"updated_at"`
	// Optional per-repository overrides for the batch flow. Nil means inherit
	// batch defaults; a non-nil empty list explicitly selects the repository
	// root or clears user exclusions, respectively.
	Paths        *[]string `json:"paths,omitempty"`
	Exclude      *[]string `json:"exclude,omitempty"`
	PreviewToken string    `json:"preview_token,omitempty"`
	AllowEmpty   bool      `json:"allow_empty,omitempty"`
}

type GitHubDiscoveryRequest struct {
	TenantID    uint64                 `json:"-"`
	Owner       string                 `json:"owner"`
	Cursor      string                 `json:"cursor,omitempty"`
	Credentials map[string]interface{} `json:"credentials,omitempty"`
}

type GitHubDiscoveryResponse struct {
	Owner        string                      `json:"owner"`
	Repositories []GitHubRepositoryCandidate `json:"repositories"`
	NextCursor   string                      `json:"next_cursor,omitempty"`
}

// GitHubBatchRequest is an explicit, bounded selection from a previously
// inspected owner. A repository remains one ordinary DataSource after create,
// so its permissions, snapshots, logs and failures stay independent.
type GitHubBatchRequest struct {
	TenantID        uint64                      `json:"-"`
	KnowledgeBaseID string                      `json:"knowledge_base_id"`
	Owner           string                      `json:"owner"`
	Repositories    []GitHubRepositoryCandidate `json:"repositories"`
	Credentials     map[string]interface{}      `json:"credentials,omitempty"`
	Mode            string                      `json:"mode"`
	Paths           []string                    `json:"paths,omitempty"`
	Exclude         []string                    `json:"exclude,omitempty"`
	// SyncPolicy makes an explicit batch choice distinguishable from an
	// omitted legacy request. "manual" persists no cron and never queues an
	// initial sync; "scheduled" requires a valid SyncSchedule. An omitted
	// policy preserves the legacy six-hour schedule behaviour. "staggered"
	// assigns each repository+mode a stable six-hour slot and rejects an
	// explicit SyncSchedule, so hand-picked cron expressions are never changed.
	SyncPolicy   string `json:"sync_policy,omitempty"`
	SyncSchedule string `json:"sync_schedule,omitempty"`
	StartSync    bool   `json:"start_sync"`
	// Reviewed flows require one valid, signed preview per selected repository.
	// Legacy callers omitting this field retain their established API behavior.
	ScopeReviewRequired bool `json:"scope_review_required,omitempty"`
}

// GitHubBatchScopePreviewRequest is read-only and contains exactly one
// candidate. No source_id is needed, so it can be used before batch creation.
type GitHubBatchScopePreviewRequest struct {
	TenantID        uint64                 `json:"-"`
	KnowledgeBaseID string                 `json:"knowledge_base_id"`
	Owner           string                 `json:"owner"`
	Repository      string                 `json:"repository"`
	Ref             string                 `json:"ref,omitempty"`
	Mode            string                 `json:"mode"`
	Paths           *[]string              `json:"paths,omitempty"`
	Exclude         *[]string              `json:"exclude,omitempty"`
	Credentials     map[string]interface{} `json:"credentials,omitempty"`
}

type GitHubBatchScopePreviewResponse struct {
	Repository   string                       `json:"repository"`
	Ref          string                       `json:"ref"`
	Mode         string                       `json:"mode"`
	Commit       string                       `json:"commit,omitempty"`
	TreeState    string                       `json:"tree_state"` // complete, truncated, missing_path, error
	Paths        []string                     `json:"paths"`
	Exclude      []string                     `json:"exclude"`
	Summary      GitHubDocumentPreviewSummary `json:"summary"`
	Estimated    bool                         `json:"estimated"`
	Warnings     []string                     `json:"warnings"`
	ErrorCode    string                       `json:"error_code,omitempty"`
	ErrorMessage string                       `json:"error_message,omitempty"`
	PreviewToken string                       `json:"preview_token,omitempty"`
	ExpiresAt    string                       `json:"expires_at,omitempty"`
}

type GitHubBatchItemResult struct {
	Repository   string `json:"repository"`
	Status       string `json:"status"`
	DataSourceID string `json:"data_source_id,omitempty"`
	Message      string `json:"message,omitempty"`
}

type GitHubBatchResponse struct {
	Owner   string                  `json:"owner"`
	Results []GitHubBatchItemResult `json:"results"`
}

// GitHubScheduleMigrationPreviewItem is a display-safe snapshot of one source.
// No credential or source configuration body is returned to the browser.
type GitHubScheduleMigrationPreviewItem struct {
	DataSourceID string    `json:"data_source_id"`
	Repository   string    `json:"repository,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	Status       string    `json:"status"`
	Current      string    `json:"current_schedule"`
	Proposed     string    `json:"proposed_schedule,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
	Eligible     bool      `json:"eligible"`
	Reason       string    `json:"reason"`
}

type GitHubScheduleMigrationPreviewRequest struct {
	TenantID        uint64 `json:"-"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
}

type GitHubScheduleMigrationPreviewResponse struct {
	KnowledgeBaseID string                               `json:"knowledge_base_id"`
	Items           []GitHubScheduleMigrationPreviewItem `json:"items"`
}

// Each apply item binds the user's explicit selection to the previewed row
// version and values. The server recomputes Proposed; it never trusts a
// client-supplied cron expression as the migration target.
type GitHubScheduleMigrationSelection struct {
	DataSourceID      string    `json:"data_source_id"`
	ExpectedSchedule  string    `json:"expected_schedule"`
	ExpectedStatus    string    `json:"expected_status"`
	ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
	ExpectedProposed  string    `json:"expected_proposed"`
}

type GitHubScheduleMigrationApplyRequest struct {
	TenantID        uint64                             `json:"-"`
	KnowledgeBaseID string                             `json:"knowledge_base_id"`
	Selections      []GitHubScheduleMigrationSelection `json:"selections"`
}

type GitHubScheduleMigrationApplyItem struct {
	DataSourceID string `json:"data_source_id"`
	Status       string `json:"status"` // applied, applied_with_warning or skipped
	Reason       string `json:"reason"`
	Schedule     string `json:"schedule,omitempty"`
}

type GitHubScheduleMigrationApplyResponse struct {
	KnowledgeBaseID string                             `json:"knowledge_base_id"`
	Results         []GitHubScheduleMigrationApplyItem `json:"results"`
}
