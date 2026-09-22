package types

import "time"

// GitHubReleaseCitationDataKey is private, turn-local provenance attached to a
// github_release_lookup result. It proves that a current-release statement came
// from GitHub's authoritative latest-release endpoint for a data source already
// authorized for this turn. Persistence and client serializers must strip it
// before a tool result leaves the agent process.
const GitHubReleaseCitationDataKey = "_github_release_citation"

// GitHubReleaseLookupDataKey is private, turn-local audit data for a completed
// call to GitHub's dedicated latest-release endpoint. It lets the evidence
// contract distinguish "no stable release was returned by the official
// endpoint" from "the model never looked". It is never serialized.
const GitHubReleaseLookupDataKey = "_github_release_lookup"

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

// GitHubReleaseLookupAudit is intentionally non-serializable. It contains no
// credentials or internal IDs; repository identity is used only inside the
// active turn when deciding whether a safe uncertainty reply may be shown.
type GitHubReleaseLookupAudit struct {
	Repository string `json:"-"`
}
