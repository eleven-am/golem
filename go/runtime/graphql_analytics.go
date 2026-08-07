package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
)

// CallerExecuteFrozenAnalytics is the generated GraphQL bridge into the same
// caller analytics execution path used by generated Go clients. It performs no
// GraphQL-specific authorization, planning, SQL rendering, or evaluation.
func CallerExecuteFrozenAnalytics[P, A any](ctx context.Context, caller *Caller[P, A], request golem.FrozenAnalyticsRequest) ([][]golem.RuntimeAnalyticsCell, error) {
	if caller == nil || caller.app == nil || caller.executor == nil || caller.policies == nil {
		return nil, fmt.Errorf("P6_GRAPHQL_ANALYTICS_RUNTIME: caller is unavailable")
	}
	return executeAnalyticsWithMode(ctx, caller.app, caller.executor, caller.policies, false, request.ModelID(), request, false)
}
