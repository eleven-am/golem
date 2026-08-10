// Package failpoint provides context-local migration boundary probes for
// internal crash-recovery tests. Production callers never install a hook, so
// Reach is a no-op and this package creates no public or process-global ABI.
package failpoint

import "context"

type hookKey struct{}

type Hook func(string)

func WithHook(ctx context.Context, hook Hook) context.Context {
	if ctx == nil || hook == nil {
		return ctx
	}
	return context.WithValue(ctx, hookKey{}, hook)
}

func Reach(ctx context.Context, boundary string) {
	if ctx == nil || boundary == "" {
		return
	}
	if hook, ok := ctx.Value(hookKey{}).(Hook); ok && hook != nil {
		hook(boundary)
	}
}
