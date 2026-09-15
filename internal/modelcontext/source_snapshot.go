package modelcontext

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
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
		// Only discover labeled KB IDs. The code body must never be mined for
		// arbitrary ID-looking strings or forged reference tags.
		r.registerStructuredReferences(output)
		return output
	}
	if action != "read" {
		return output
	}
	var read types.SourceRead
	if json.Unmarshal([]byte(output), &read) != nil {
		return output
	}
	u := read.SourceURL
	if u == "" {
		u = read.PreviewURL
	}
	handle := r.registerFileReference(u, fmt.Sprintf("%s:%d-%d", read.Path, read.StartLine, read.EndLine))
	metadata, _ := json.Marshal(map[string]interface{}{"source_id": read.DataSourceID, "snapshot_id": read.SnapshotID, "path": read.Path, "revision": read.Revision, "start_line": read.StartLine, "end_line": read.EndLine, "total_lines": read.TotalLines, "truncated": read.Truncated, "citation_ref": handle})
	var body bytes.Buffer
	_ = xml.EscapeText(&body, []byte(read.Content))
	return string(metadata) + fmt.Sprintf("\n<source_file ref=\"%s\">\n%s\n</source_file>\nCite this excerpt with <ref id=\"%s\"/>.", handle, body.String(), handle)
}
