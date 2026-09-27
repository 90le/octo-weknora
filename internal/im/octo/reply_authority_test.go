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

func TestReplyAuthorityRejectsDraftAfterManagementRevocationWithReadBindingRetained(t *testing.T) {
	old := &im.ExecutionScope{AccountID: "a", ScopeID: "s", Revision: "r", KnowledgeBaseIDs: []string{"kb"}, ManageKnowledgeBaseIDs: []string{"kb"}, CanManageScope: true}
	current := *old
	if !permitsReply(encodeAuthority(old), &current) {
		t.Fatal("unchanged manager denied")
	}
	current.ManageKnowledgeBaseIDs = nil
	if permitsReply(encodeAuthority(old), &current) {
		t.Fatal("draft disclosed after management grant revoked while public query remained")
	}
	current = *old
	current.CanManageScope = false
	if permitsReply(encodeAuthority(old), &current) {
		t.Fatal("draft disclosed after native group manager role lost")
	}
	current = *old
	current.KnowledgeBaseIDs = nil
	if !permitsReply(encodeAuthority(old), &current) {
		t.Fatal("explicit management-only asset lost authorized maintenance receipt")
	}
}

func TestReplyAuthorityRejectsRevokedPublicWebEvidence(t *testing.T) {
	old := &im.ExecutionScope{AccountID: "a", ScopeID: "group", Revision: "r", KnowledgeBaseIDs: []string{"kb"}, AllowPublicWeb: true}
	current := *old
	if !permitsReply(encodeAuthority(old), &current) {
		t.Fatal("unchanged public web scope denied")
	}
	current.AllowPublicWeb = false
	if permitsReply(encodeAuthority(old), &current) {
		t.Fatal("external evidence delivered after public web revocation")
	}
	old.AllowPublicWeb = false
	current.AllowPublicWeb = true
	if !permitsReply(encodeAuthority(old), &current) {
		t.Fatal("new grant unnecessarily invalidated already private answer")
	}
}
