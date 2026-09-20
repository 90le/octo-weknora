package octo

import (
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/im"
)

func encodeAuthority(scope *im.ExecutionScope) string { b, _ := json.Marshal(scope); return string(b) }

// Successful management can add a KB or remove only its query binding while
// retaining the administrator's asset grant. That must not hide the receipt.
// Loss of any knowledge used to produce an answer still rejects deferred output.
func permitsReply(authority string, current *im.ExecutionScope) bool {
	var old im.ExecutionScope
	if authority == "" || current == nil || json.Unmarshal([]byte(authority), &old) != nil {
		return false
	}
	if old.AccountID != current.AccountID || old.ScopeID != current.ScopeID || old.Revision != current.Revision {
		return false
	}
	knowledge := append(append([]string(nil), current.KnowledgeBaseIDs...), current.ManageKnowledgeBaseIDs...)
	if !subset(old.KnowledgeBaseIDs, knowledge) || !subset(old.ReadIssueScopeIDs, current.ReadIssueScopeIDs) {
		return false
	}
	issueKnowledge := append(append([]string(nil), current.KnowledgeBaseIDs...), current.ReadIssueKnowledgeBaseIDs...)
	return subset(old.ReadIssueKnowledgeBaseIDs, issueKnowledge)
}

func subset(need, available []string) bool {
	set := map[string]bool{}
	for _, id := range available {
		set[id] = true
	}
	for _, id := range need {
		if !set[id] {
			return false
		}
	}
	return true
}
