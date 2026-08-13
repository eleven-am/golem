package failpoint

import (
	"context"
	"testing"
)

func TestHookIsContextLocalAndIgnoresEmptyBoundaries(t *testing.T) {
	var reached []string
	ctx := WithHook(context.Background(), func(boundary string) { reached = append(reached, boundary) })
	Reach(ctx, "before-first-phase")
	Reach(ctx, "")
	Reach(context.Background(), "foreign")
	if len(reached) != 1 || reached[0] != "before-first-phase" {
		t.Fatalf("reached=%v", reached)
	}
}
