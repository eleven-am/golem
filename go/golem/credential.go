package golem

import "context"

type noCredentialExpiryKey struct{}

func WithoutCredentialExpiry(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCredentialExpiryKey{}, true)
}

func HasNoCredentialExpiry(ctx context.Context) bool {
	marked, _ := ctx.Value(noCredentialExpiryKey{}).(bool)
	return marked
}
