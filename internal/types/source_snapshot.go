package types

import "time"

// SourceFile is a text file in an immutable source snapshot. Object is a content
// hash, never a caller-supplied filesystem path.
type SourceFile struct {
	Path      string `json:"path"`
	Object    string `json:"object"`
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
