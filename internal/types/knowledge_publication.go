package types

import "encoding/json"

// IsPublishedKnowledgeForAnswer is shared by retrieval and public IM tools.
// Preparing repository candidates and manual drafts must not become answers.
func IsPublishedKnowledgeForAnswer(k *Knowledge) bool {
	if k == nil || k.EnableStatus != "enabled" {
		return false
	}
	if k.Channel == ConnectorTypeGitHub || k.Channel == "local_folder" {
		var metadata map[string]string
		if json.Unmarshal(k.Metadata, &metadata) != nil || metadata["sync_target_external_id"] != "" {
			return false
		}
	}
	if k.IsManual() {
		meta, err := k.ManualMetadata()
		return err == nil && meta != nil && meta.Status == ManualKnowledgeStatusPublish
	}
	return true
}
