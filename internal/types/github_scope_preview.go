package types

// GitHubDocumentScopePreviewRequest previews a proposed document selection for
// one existing, authorized GitHub data source. Nil paths/exclude means "use the
// stored setting"; an explicit empty array means the whole repository/no
// exclusions respectively. This request never updates the data source.
type GitHubDocumentScopePreviewRequest struct {
	SourceID string    `json:"source_id"`
	Paths    *[]string `json:"paths,omitempty"`
	Exclude  *[]string `json:"exclude,omitempty"`
}

type GitHubPreviewGroup struct {
	Name  string `json:"name"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// GitHubDocumentPreviewSummary counts repository-tree metadata only. Bytes are
// Git blob sizes, not downloaded bytes; actual incremental sync may skip
// unchanged files. CandidateFiles uses the connector's current document
// selector after the mandatory sensitive-path rule, including files the
// downstream parser cannot yet import. Sensitive candidates appear only as
// aggregate count/bytes; no sensitive path is serialized.
type GitHubDocumentPreviewSummary struct {
	CandidateFiles          int                  `json:"candidate_files"`
	CandidateBytes          int64                `json:"candidate_bytes"`
	EligibleFiles           int                  `json:"eligible_files"`
	EligibleBytes           int64                `json:"eligible_bytes"`
	ImageFiles              int                  `json:"image_files"`
	ImageBytes              int64                `json:"image_bytes"`
	SensitiveCandidateFiles int                  `json:"sensitive_candidate_files"`
	SensitiveCandidateBytes int64                `json:"sensitive_candidate_bytes"`
	ParserUnsupportedFiles  int                  `json:"parser_unsupported_files"`
	ParserUnsupportedBytes  int64                `json:"parser_unsupported_bytes"`
	TooLargeFiles           int                  `json:"too_large_files"`
	TooLargeBytes           int64                `json:"too_large_bytes"`
	Extensions              []GitHubPreviewGroup `json:"extensions"`
	TopDirectories          []GitHubPreviewGroup `json:"top_directories"`
	SamplePaths             []string             `json:"sample_paths"`
	Warnings                []string             `json:"warnings"`
}

// GitHubDocumentScopePreview is an authorization-scoped, read-only dry run.
// ActualSync reflects today's document connector: paths are applied, exclude
// rules are not. ProposedAfterExclude is planning information only and must
// never be presented as an active sync policy until the connector supports it.
type GitHubDocumentScopePreview struct {
	SourceID                string                        `json:"source_id"`
	Repository              string                        `json:"repository,omitempty"`
	Ref                     string                        `json:"ref,omitempty"`
	Commit                  string                        `json:"commit,omitempty"`
	TreeState               string                        `json:"tree_state"` // complete, truncated, missing_path, error
	TreeEntries             int                           `json:"tree_entries"`
	StoredPaths             []string                      `json:"stored_paths"`
	PreviewPaths            []string                      `json:"preview_paths"`
	PathsOverridden         bool                          `json:"paths_overridden"`
	FullRepository          bool                          `json:"full_repository"`
	ProposedExclude         []string                      `json:"proposed_exclude"`
	RedactedSelectionPaths  int                           `json:"redacted_selection_paths"`
	ExcludeOverridden       bool                          `json:"exclude_overridden"`
	ExclusionsAppliedBySync bool                          `json:"exclusions_applied_by_sync"`
	ActualSync              GitHubDocumentPreviewSummary  `json:"actual_sync"`
	ProposedAfterExclude    *GitHubDocumentPreviewSummary `json:"proposed_after_exclude,omitempty"`
	Warnings                []string                      `json:"warnings"`
	ErrorCode               string                        `json:"error_code,omitempty"`
	ErrorMessage            string                        `json:"error_message,omitempty"`
}
