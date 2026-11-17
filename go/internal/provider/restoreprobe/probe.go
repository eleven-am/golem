// Package restoreprobe owns context-local boundaries for internal SQLite backup
// and restore tests. Production contexts install no hook; there is no public or global API.
package restoreprobe

import "context"

type key struct{}
type Hook func(string)

// BackupStepIncomplete follows a successful 64-page step with pages remaining.
const BackupStepIncomplete = "backup_step_incomplete"

const BeforePublication = "before_publication"
const AfterPublication = "after_publication_before_directory_sync"

func WithHook(ctx context.Context, hook Hook) context.Context {
	if ctx == nil || hook == nil {
		return ctx
	}
	return context.WithValue(ctx, key{}, hook)
}
func Reach(ctx context.Context, boundary string) {
	if ctx == nil {
		return
	}
	if hook, ok := ctx.Value(key{}).(Hook); ok && hook != nil {
		hook(boundary)
	}
}
