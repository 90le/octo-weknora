package types

import "context"

type imKnowledgeScopeKey struct{}
type imPublicWebAuthorizerKey struct{}

// IMWebAuthorizer rechecks a native IM scope immediately before public network
// access. It is provided by the admitted channel, not by a model or user prompt.
type IMWebAuthorizer func(context.Context) error

func WithIMPublicWebAuthorizer(ctx context.Context, authorize IMWebAuthorizer) context.Context {
	if authorize == nil {
		return ctx
	}
	return context.WithValue(ctx, imPublicWebAuthorizerKey{}, authorize)
}

func IMPublicWebAuthorizer(ctx context.Context) IMWebAuthorizer {
	authorize, _ := ctx.Value(imPublicWebAuthorizerKey{}).(IMWebAuthorizer)
	return authorize
}

func HasIMKnowledgeScope(ctx context.Context) bool {
	_, ok := ctx.Value(imKnowledgeScopeKey{}).([]string)
	return ok
}

// WithIMKnowledgeScope is set by the native IM admission policy, never by a
// model argument or HTTP identity header. A copy prevents later scope mutation.
func WithIMKnowledgeScope(ctx context.Context, ids []string) context.Context {
	return context.WithValue(ctx, imKnowledgeScopeKey{}, append([]string(nil), ids...))
}

func RestrictIMAgentConfig(ctx context.Context, config *AgentConfig) *AgentConfig {
	ids, ok := ctx.Value(imKnowledgeScopeKey{}).([]string)
	if !ok || config == nil {
		return config
	}
	out := *config
	out.KnowledgeBases = append([]string(nil), ids...)
	out.KnowledgeIDs = nil
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	out.SearchTargets = nil
	for _, target := range config.SearchTargets {
		if target != nil && allowed[target.KnowledgeBaseID] {
			out.SearchTargets = append(out.SearchTargets, target)
		}
	}
	out.LocalBrowserEnabled = false
	// A saved Agent switch alone never grants a public IM channel network access.
	// The trusted channel must also attach an exact-scope reauthorizer.
	out.WebSearchEnabled = out.WebSearchEnabled && IMPublicWebAuthorizer(ctx) != nil
	out.MCPSelectionMode = "none"
	out.MCPServices = nil
	out.PinnedMCPServiceIDs = nil
	grant, granted := IMSandboxGrantFromContext(ctx)
	if !granted || (!grant.AllowShell && !grant.AllowSkills) || config.SandboxConfigID != grant.ConfigID {
		out.SandboxConfigID = ""
	}
	if out.SandboxConfigID == "" || !granted || !grant.AllowSkills {
		out.SkillsEnabled = false
		out.AllowedSkills = nil
		out.TenantSkills = nil
		out.PinnedSkillNames = nil
	}
	// Never consult a local host skill tree in an IM turn.
	out.SkillDirs = nil
	off := false
	out.MemoryEnabled = &off
	out.SharedAgentReadOnly = true
	return &out
}
