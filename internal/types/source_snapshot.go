package types

import "time"

// SourceFile is a text file in an immutable source snapshot. Object is a content
// hash, never a caller-supplied filesystem path.
type SourceFile struct {
	Path   string `json:"path"`
	Object string `json:"object"`
	// GitBlob is the immutable Git blob object ID when this file came from a
	// Git source. It lets a later source snapshot reuse an already verified
	// content-addressed object without rereading an unchanged blob.
	GitBlob   string `json:"git_blob,omitempty"`
	Size      int64  `json:"size"`
	Lines     int    `json:"lines"`
	SourceURL string `json:"source_url,omitempty"`
	Revision  string `json:"revision,omitempty"`
}
type SourceSnapshot struct {
	ID              string         `json:"id"`
	TenantID        uint64         `json:"tenant_id"`
	KnowledgeBaseID string         `json:"knowledge_base_id"`
	DataSourceID    string         `json:"data_source_id"`
	Selection       string         `json:"selection"`
	Revision        string         `json:"revision"`
	CreatedAt       time.Time      `json:"created_at"`
	Files           []SourceFile   `json:"files"`
	Skipped         map[string]int `json:"skipped"`
}
type SourceSummary struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Status     string         `json:"status"`
	SnapshotID string         `json:"snapshot_id,omitempty"`
	Revision   string         `json:"revision,omitempty"`
	FileCount  int            `json:"file_count"`
	Skipped    map[string]int `json:"skipped,omitempty"`
	CreatedAt  *time.Time     `json:"created_at,omitempty"`
}
type SourceTreeEntry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size,omitempty"`
}
type SourceTree struct {
	SnapshotID string            `json:"snapshot_id"`
	Entries    []SourceTreeEntry `json:"entries"`
	Total      int               `json:"total"`
}
type SourceRead struct {
	DataSourceID string `json:"source_id"`
	SnapshotID   string `json:"snapshot_id"`
	Path         string `json:"path"`
	Revision     string `json:"revision"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	TotalLines   int    `json:"total_lines"`
	Content      string `json:"content"`
	Truncated    bool   `json:"truncated"`
	SourceURL    string `json:"source_url,omitempty"`
	PreviewURL   string `json:"preview_url"`
}

// SourceBrowseCitationDataKey is private, turn-local provenance attached to a
// source_browse read result. It lets trusted transport code prove that a public
// source URL came from an authorized source read without disclosing the
// knowledge-base ID to the model, client, or persisted agent history.
const SourceBrowseCitationDataKey = "_source_browse_citation"

// SourceBrowseSearchDataKey is private, turn-local audit data attached to a
// successful source_browse search. It records that an authorized search ran,
// while deliberately keeping a search-only result distinct from read evidence.
// Persistence and client serializers must strip it before a tool result leaves
// the agent process.
const SourceBrowseSearchDataKey = "_source_browse_search"

// SourceBrowseCitation is intentionally non-serializable. It is held only in
// ToolResult.Data during the live turn; persistence and client sanitizers drop
// its key before the result leaves the agent process.
type SourceBrowseCitation struct {
	KnowledgeBaseID string `json:"-"`
	Repository      string `json:"-"`
	URL             string `json:"-"`
	Path            string `json:"-"`
	Revision        string `json:"-"`
}

// SourceBrowseSearchAudit is intentionally non-serializable. A completed
// zero-hit search proves only that a bounded lookup ran; it never establishes
// a source fact or authorizes automatic knowledge-gap registration.
type SourceBrowseSearchAudit struct {
	Repository string `json:"-"`
	Complete   bool   `json:"-"`
	Matched    bool   `json:"-"`
}

type SourceMatch struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Text      string `json:"text"`
	SourceURL string `json:"source_url,omitempty"`
}
type SourceSearch struct {
	SnapshotID   string        `json:"snapshot_id"`
	Matches      []SourceMatch `json:"matches"`
	ScannedFiles int           `json:"scanned_files"`
	Complete     bool          `json:"complete"`
}
