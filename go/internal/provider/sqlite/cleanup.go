package sqlite

import (
	"context"
	"time"
)

const sqliteCleanupTimeout = 5 * time.Second

func sqliteCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, sqliteCleanupTimeout)
}
