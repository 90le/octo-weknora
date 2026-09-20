package octo

import (
	"github.com/Tencent/WeKnora/internal/im"
	"testing"
)

func TestReplyAuthorityAllowsSuccessfulBindingButNotPartialRevocation(t *testing.T) {
	old := &im.ExecutionScope{AccountID: "a", ScopeID: "s", Revision: "r", KnowledgeBaseIDs: []string{"a", "b"}, ReadIssueScopeIDs: []string{"s"}}
	current := *old
	current.KnowledgeBaseIDs = []string{"a", "b", "created"}
	if !permitsReply(encodeAuthority(old), &current) {
		t.Fatal("successful creation hid its receipt")
	}
	current.KnowledgeBaseIDs = []string{"b"}
	if permitsReply(encodeAuthority(old), &current) {
		t.Fatal("removed KB answer disclosed through remaining KB")
	}
	current.ManageKnowledgeBaseIDs = []string{"a"}
	if !permitsReply(encodeAuthority(old), &current) {
		t.Fatal("authorized administrator lost unbind receipt")
	}
	current.ScopeID = "other"
	if permitsReply(encodeAuthority(old), &current) {
		t.Fatal("scope changed")
	}
}
