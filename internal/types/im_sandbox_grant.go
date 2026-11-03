package types

import (
	"context"
	"errors"
)

// IMSandboxGrant is bound by the trusted IM adapter to one native principal,
// channel session, Agent and explicitly selected sandbox. It is not a file
// delivery grant. Every operation must call Revalidate and verify the pin.
type IMSandboxGrant struct {
	SessionID  string
	TenantID   uint64
	ConfigID   string
	Revalidate func(context.Context) error
}

type imSandboxGrantKey struct{}

// WithIMSandboxGrant is for the trusted IM admission path, not user input.
func WithIMSandboxGrant(ctx context.Context, grant IMSandboxGrant) context.Context {
	return context.WithValue(ctx, imSandboxGrantKey{}, grant)
}

func IMSandboxGrantFromContext(ctx context.Context) (IMSandboxGrant, bool) {
	grant, ok := ctx.Value(imSandboxGrantKey{}).(IMSandboxGrant)
	return grant, ok && grant.SessionID != "" && grant.TenantID != 0 && grant.ConfigID != "" && grant.Revalidate != nil
}

var ErrIMSandboxDenied = errors.New("IM sandbox access denied")
