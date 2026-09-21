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
	SyncSchedule    string                      `json:"sync_schedule,omitempty"`
	StartSync       bool                        `json:"start_sync"`
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
