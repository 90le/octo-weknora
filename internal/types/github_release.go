package types

import "time"

// GitHubReleaseCitationDataKey is private, turn-local provenance attached to a
// github_release_lookup result. It proves that a current-release statement came
// from GitHub's authoritative latest-release endpoint for a data source already
// authorized for this turn. Persistence and client serializers must strip it
// before a tool result leaves the agent process.
const GitHubReleaseCitationDataKey = "_github_release_citation"

// GitHubReleaseCitation intentionally does not serialize. The model receives
// the public repository, tag, release URL and checked time in the tool output,
// but never the backing knowledge-base or data-source identifiers.
type GitHubReleaseCitation struct {
	KnowledgeBaseID string    `json:"-"`
	DataSourceID    string    `json:"-"`
	Repository      string    `json:"-"`
	TagName         string    `json:"-"`
	URL             string    `json:"-"`
	PublishedAt     time.Time `json:"-"`
	CheckedAt       time.Time `json:"-"`
}
