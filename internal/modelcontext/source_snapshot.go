package modelcontext

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
)

// IM accepts a short subset of lines from one observed source read. Give the
// model the same citation boundary up front so a broad exploratory read cannot
// turn into one citation purportedly supporting many separate claims.
const maxCitableSourceReadLines = 12

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
	// A broad read is still useful for navigation, but only an exact, nonblank
	// 1–12-line window can support a citation. Check the returned content as
	// well as metadata: a malformed range must not mint a handle either.
	handle := ""
	nonblank := strings.TrimSpace(read.Content) != ""
	lineCount := 0
	if read.Content != "" {
		lineCount = strings.Count(read.Content, "\n") + 1
	}
	citableWindow := nonblank && read.StartLine > 0 && read.EndLine >= read.StartLine &&
		read.EndLine-read.StartLine < maxCitableSourceReadLines &&
		read.EndLine-read.StartLine+1 == lineCount
	if citableWindow {
		handle = r.registerFileReference(u, fmt.Sprintf("%s:%d-%d", read.Path, read.StartLine, read.EndLine))
	}
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
	if read.Content != "" {
		start := read.StartLine
		if start < 1 { // Preserve old transcripts that omitted start_line.
			start = 1
		}
		for i, line := range strings.Split(read.Content, "\n") {
			if i > 0 {
				body.WriteByte('\n')
			}
			fmt.Fprintf(&body, "L%d: ", start+i)
			_ = xml.EscapeText(&body, []byte(line))
		}
	}
	if handle == "" {
		if nonblank && !citableWindow {
			return string(encodedMetadata) + fmt.Sprintf("\n<source_file>\n%s\n</source_file>\nThis broad or inconsistent read is for exploration only. To cite a claim, call source_browse.read again with an exact start_line/end_line window of at most 12 original lines, then use only the new citation_ref.", body.String())
		}
		return string(encodedMetadata) + fmt.Sprintf("\n<source_file>\n%s\n</source_file>\nNo citable source reference is available for this excerpt.", body.String())
	}
	return string(encodedMetadata) + fmt.Sprintf("\n<source_file ref=\"%s\">\n%s\n</source_file>\nCite this excerpt with <ref id=\"%s\"/>.", handle, body.String(), handle)
}
