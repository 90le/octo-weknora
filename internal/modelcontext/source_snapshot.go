package modelcontext

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
)

// Code citations retain line fragments. Ordinary web references deduplicate
// without fragments, which would send two different code excerpts to one line.
func (r *sourceRegistry) registerFileReference(rawURL, title string) string {
	if rawURL == "" {
		return ""
	}
	handle := r.webs.register("source-file:"+rawURL, rawURL, webMeta{title: title}, func(dst *webMeta, src webMeta) {
		if dst.title == "" {
			dst.title = src.title
		}
	})
	r.citable.Store(handle, true)
	return handle
}
func (r *sourceRegistry) modelSourceSnapshot(action, output string) string {
	if action == "list" {
		// New source_browse catalogs contain only per-tool source_ref handles.
		// Keep this legacy registration path for historical transcripts that
		// still carry labeled KB IDs; it is not used by current catalog output.
		r.registerStructuredReferences(output)
		return output
	}
	if action != "read" {
		return output
	}
	var read struct {
		SourceRef  string          `json:"source_ref"`
		Repository string          `json:"repository"`
		Snapshot   json.RawMessage `json:"snapshot"`
		Path       string          `json:"path"`
		Revision   string          `json:"revision"`
		StartLine  int             `json:"start_line"`
		EndLine    int             `json:"end_line"`
		TotalLines int             `json:"total_lines"`
		Content    string          `json:"content"`
		Truncated  bool            `json:"truncated"`
		SourceURL  string          `json:"source_url"`
		PreviewURL string          `json:"preview_url"`
	}
	if json.Unmarshal([]byte(output), &read) != nil {
		return output
	}
	u := read.SourceURL
	if u == "" {
		u = read.PreviewURL
	}
	handle := r.registerFileReference(u, fmt.Sprintf("%s:%d-%d", read.Path, read.StartLine, read.EndLine))
	metadata := map[string]interface{}{"path": read.Path, "revision": read.Revision, "start_line": read.StartLine, "end_line": read.EndLine, "total_lines": read.TotalLines, "truncated": read.Truncated, "citation_ref": handle}
	if read.SourceRef != "" {
		metadata["source_ref"] = read.SourceRef
	}
	if read.Repository != "" {
		metadata["repository"] = read.Repository
	}
	if len(read.Snapshot) > 0 && string(read.Snapshot) != "null" {
		metadata["snapshot"] = json.RawMessage(read.Snapshot)
	}
	encodedMetadata, _ := json.Marshal(metadata)
	var body bytes.Buffer
	_ = xml.EscapeText(&body, []byte(read.Content))
	return string(encodedMetadata) + fmt.Sprintf("\n<source_file ref=\"%s\">\n%s\n</source_file>\nCite this excerpt with <ref id=\"%s\"/>.", handle, body.String(), handle)
}
