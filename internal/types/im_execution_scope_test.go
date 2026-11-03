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

func TestIMScopeDisablesAgentSkillsEvenWhenRuntimeWasPreconfigured(t *testing.T) {
	in := &AgentConfig{
		SkillsEnabled:    true,
		AllowedSkills:    []string{"allowed"},
		SandboxConfigID:  "remote",
		PinnedSkillNames: []string{"allowed"},
	}
	out := RestrictIMAgentConfig(WithIMKnowledgeScope(context.Background(), []string{"kb"}), in)
	if out.SkillsEnabled || len(out.AllowedSkills) > 0 || len(out.PinnedSkillNames) > 0 || out.SandboxConfigID != "" {
		t.Fatalf("skills or sandbox remained enabled: %+v", out)
	}
	if !in.SkillsEnabled || in.SandboxConfigID != "remote" {
		t.Fatal("original mutated")
	}
}

func TestIMSandboxAndSkillSwitchesIntersectAgent(t *testing.T) {
	ctx := WithIMKnowledgeScope(context.Background(), []string{"kb"})
	in := &AgentConfig{
		SandboxConfigID: "cfg",
		SkillsEnabled:   true,
		AllowedSkills:   []string{"chosen"},
		SkillDirs:       []string{"host"},
	}
	grant := IMSandboxGrant{
		SessionID:  "s",
		TenantID:   7,
		ConfigID:   "cfg",
		Revalidate: func(context.Context) error { return nil },
	}
	for _, tc := range []struct{ shell, skills bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		grant.AllowShell, grant.AllowSkills = tc.shell, tc.skills
		out := RestrictIMAgentConfig(WithIMSandboxGrant(ctx, grant), in)
		if (out.SandboxConfigID != "") != (tc.shell || tc.skills) || out.SkillsEnabled != tc.skills ||
			len(out.SkillDirs) != 0 {
			t.Fatalf("switch intersection failed: %+v", out)
		}
	}
	grant.ConfigID = "other"
	grant.AllowShell, grant.AllowSkills = true, true
	out := RestrictIMAgentConfig(WithIMSandboxGrant(ctx, grant), in)
	if out.SandboxConfigID != "" || out.SkillsEnabled {
		t.Fatal("mismatched config admitted")
	}
	if RestrictIMAgentConfig(context.Background(), in) != in {
		t.Fatal("unscoped channels changed")
	}
}
