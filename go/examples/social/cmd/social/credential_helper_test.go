package main

import (
	"context"
	"time"

	"github.com/eleven-am/golem/go/examples/social/social"
)

type testDeadlineContext struct{ context.Context }

func (testDeadlineContext) Deadline() (time.Time, bool) { return time.Now().Add(time.Hour), true }

func testPrincipalContext(ctx context.Context, principal social.Principal) context.Context {
	return testDeadlineContext{context.WithValue(ctx, principalContextKey{}, principal)}
}
