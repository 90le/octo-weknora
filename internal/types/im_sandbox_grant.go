package types

import (
	"context"
	"errors"
)

// IMSandboxGrant is bound by the trusted IM adapter to one native principal,
// channel session, Agent and explicitly selected sandbox. It is not a file
// delivery grant. Every operation must call Revalidate and verify the pin.
type IMSandboxGrant struct {
	SessionID           string
	TenantID            uint64
	ConfigID            string
	AgentConfigDigest   string
	SkillsSelectionMode string
	SelectedSkillNames  []string
	// These are admitted channel switches, not a substitute for Revalidate.
	AllowShell      bool
	AllowSkills     bool
	SkillsRequested bool
	TurnBarrier     *IMSandboxTurnBarrier
	Revalidate      func(context.Context) error
}

type imSandboxPolicyKey struct{}

// WithIMSandboxPolicy attaches the fingerprint of the admitted Agent, sandbox
// and installed Skill state to the current turn. Callers must still revalidate.
func WithIMSandboxPolicy(ctx context.Context, fingerprint string) context.Context {
	return context.WithValue(ctx, imSandboxPolicyKey{}, fingerprint)
}

// IMSandboxPolicyFromContext returns the admitted policy fingerprint, if present.
func IMSandboxPolicyFromContext(ctx context.Context) string {
	value, _ := ctx.Value(imSandboxPolicyKey{}).(string)
	return value
}

type (
	imSandboxGrantKey    struct{}
	imSandboxRequestsKey struct{}
)

// IMSandboxRequests records the explicit channel requests even if no valid
// Agent sandbox was selected, so misconfiguration can be reported clearly.
type IMSandboxRequests struct{ Shell, Skills bool }

// WithIMSandboxRequests attaches the explicit channel requests to the context.
func WithIMSandboxRequests(ctx context.Context, shell, skills bool) context.Context {
	return context.WithValue(ctx, imSandboxRequestsKey{}, IMSandboxRequests{Shell: shell, Skills: skills})
}

// IMSandboxRequestsFromContext returns the channel requests, if present.
func IMSandboxRequestsFromContext(ctx context.Context) IMSandboxRequests {
	value, _ := ctx.Value(imSandboxRequestsKey{}).(IMSandboxRequests)
	return value
}

// WithoutIMSandboxExecution masks any inherited sandbox authority for this
// turn while preserving the principal and knowledge scope in the parent.
func WithoutIMSandboxExecution(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, imSandboxGrantKey{}, IMSandboxGrant{})
	ctx = WithIMSandboxRequests(ctx, false, false)
	return WithIMSandboxPolicy(ctx, "")
}

// WithIMSandboxGrant is for the trusted IM admission path, not user input.
func WithIMSandboxGrant(ctx context.Context, grant IMSandboxGrant) context.Context {
	grant.SelectedSkillNames = append([]string(nil), grant.SelectedSkillNames...)
	if grant.TurnBarrier == nil {
		grant.TurnBarrier = NewIMSandboxTurnBarrier()
	}
	return context.WithValue(ctx, imSandboxGrantKey{}, grant)
}

// IMSandboxGrantFromContext returns a grant and whether its required fields are present.
func IMSandboxGrantFromContext(ctx context.Context) (IMSandboxGrant, bool) {
	grant, ok := ctx.Value(imSandboxGrantKey{}).(IMSandboxGrant)
	grant.SelectedSkillNames = append([]string(nil), grant.SelectedSkillNames...)
	return grant, ok && grant.SessionID != "" && grant.TenantID != 0 && grant.ConfigID != "" && grant.Revalidate != nil
}

// ErrIMSandboxDenied indicates that access to the IM sandbox was denied.
var ErrIMSandboxDenied = errors.New("IM sandbox access denied")
