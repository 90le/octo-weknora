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

func TestIMPublicWebNeedsTrustedAuthorizerAndSavedAgentSwitch(t *testing.T) {
	base := WithIMKnowledgeScope(context.Background(), []string{"kb"})
	authorized := WithIMPublicWebAuthorizer(base, func(context.Context) error { return nil })
	if got := RestrictIMAgentConfig(authorized, &AgentConfig{WebSearchEnabled: true}); !got.WebSearchEnabled {
		t.Fatal("explicit native public-web permission was discarded")
	}
	if got := RestrictIMAgentConfig(authorized, &AgentConfig{WebSearchEnabled: false}); got.WebSearchEnabled {
		t.Fatal("scope permission overrode the saved Agent switch")
	}
	if IMPublicWebAuthorizer(base) != nil {
		t.Fatal("unconfigured scope has a public-web authorizer")
	}
}
