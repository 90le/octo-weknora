package tools

import "testing"

func TestGitHubReleaseLookupIsConcurrentRead(t *testing.T) {
	if !CanRunConcurrently(ToolGitHubReleaseLookup) {
		t.Fatal("GitHub release lookup is a scoped read and should not serialize unrelated reads")
	}
}
