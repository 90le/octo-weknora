package types

import (
	"context"
	"testing"
)

func TestIMScopeRestrictsRuntimeWithoutMutatingAgent(t *testing.T) {
	config := &AgentConfig{KnowledgeBases: []string{"private"}, KnowledgeIDs: []string{"doc"}, LocalBrowserEnabled: true, MCPSelectionMode: "all", SandboxConfigID: "host", PinnedMCPServiceIDs: []string{"external"}, WebSearchEnabled: true}
	if RestrictIMAgentConfig(context.Background(), config) != config {
		t.Fatal("unrelated Agent changed")
	}
	ids := []string{"allowed"}
	ctx := WithIMKnowledgeScope(context.Background(), ids)
	ids[0] = "forged"
	out := RestrictIMAgentConfig(ctx, config)
	if out.KnowledgeBases[0] != "allowed" || len(out.KnowledgeIDs) != 0 || out.LocalBrowserEnabled || out.WebSearchEnabled || out.SandboxConfigID != "" || out.MCPSelectionMode != "none" || len(out.PinnedMCPServiceIDs) != 0 {
		t.Fatal("scope bypass")
	}
	if out.MemoryEnabled == nil || *out.MemoryEnabled {
		t.Fatal("unscoped memory enabled")
	}
	if config.KnowledgeBases[0] != "private" || !config.LocalBrowserEnabled {
		t.Fatal("shared config changed")
	}
}
