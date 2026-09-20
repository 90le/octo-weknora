package octobusiness

import "context"

type reportAuthorityKey struct{}

// WithReportAuthority fixes the authorization used while querying a report.
// The sink must compare it again after upload and before the message is sent.
func WithReportAuthority(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, reportAuthorityKey{}, reportAuthority(p))
}
func ValidateReportAuthority(ctx context.Context) error {
	expected, ok := ctx.Value(reportAuthorityKey{}).(string)
	if !ok {
		return nil
	} // Non-report artifacts keep their ordinary authorization.
	current, err := currentPrincipal(ctx)
	if err != nil || reportAuthority(current) != expected {
		return ErrDenied
	}
	return nil
}
