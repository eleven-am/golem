package gentest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Difference is a normalized semantic mismatch reported by provider schema
// comparison. It contains no rendered SQL or driver-specific error value.
type Difference struct {
	Code     string
	Path     string
	Expected string
	Actual   string
}

// ProviderHarness is the provider-neutral apply/introspect/compare seam used by
// P1 verification. Plan and Schema remain generic so gentest does not own either
// compiler IR or provider semantics.
type ProviderHarness[Plan, Schema any] interface {
	Apply(context.Context, Plan) error
	Introspect(context.Context) (Schema, error)
	Compare(expected, actual Schema) []Difference
}

// VerifyProvider applies plan, introspects the resulting schema, and compares it
// with expected. Differences are sorted before returning.
func VerifyProvider[Plan, Schema any](
	ctx context.Context,
	provider ProviderHarness[Plan, Schema],
	plan Plan,
	expected Schema,
) ([]Difference, error) {
	if provider == nil {
		return nil, fmt.Errorf("provider harness is nil")
	}
	if err := provider.Apply(ctx, plan); err != nil {
		return nil, fmt.Errorf("apply provider plan: %w", err)
	}
	actual, err := provider.Introspect(ctx)
	if err != nil {
		return nil, fmt.Errorf("introspect provider schema: %w", err)
	}
	differences := append([]Difference(nil), provider.Compare(expected, actual)...)
	slices.SortStableFunc(differences, compareDifference)
	return differences, nil
}

// RequireProviderVerified is the testing.TB adapter for VerifyProvider.
func RequireProviderVerified[Plan, Schema any](
	t testing.TB,
	ctx context.Context,
	provider ProviderHarness[Plan, Schema],
	plan Plan,
	expected Schema,
) {
	t.Helper()
	differences, err := VerifyProvider(ctx, provider, plan, expected)
	if err != nil {
		t.Fatal(err)
	}
	if len(differences) == 0 {
		return
	}
	var message strings.Builder
	message.WriteString("provider schema differs:")
	for _, difference := range differences {
		fmt.Fprintf(
			&message,
			"\n  %s %s: expected %q, actual %q",
			difference.Code,
			difference.Path,
			difference.Expected,
			difference.Actual,
		)
	}
	t.Fatal(message.String())
}

func compareDifference(left, right Difference) int {
	for _, pair := range [][2]string{
		{left.Path, right.Path},
		{left.Code, right.Code},
		{left.Expected, right.Expected},
		{left.Actual, right.Actual},
	} {
		if compared := strings.Compare(pair[0], pair[1]); compared != 0 {
			return compared
		}
	}
	return 0
}

// FakeProvider implements ProviderHarness. It serializes internal call
// recording and is intentionally function-backed; it does not infer provider
// behavior. Tests should inspect exported counters only after calls finish.
type FakeProvider[Plan, Schema any] struct {
	ApplyFunc      func(context.Context, Plan) error
	IntrospectFunc func(context.Context) (Schema, error)
	CompareFunc    func(expected, actual Schema) []Difference

	mu              sync.Mutex
	AppliedPlans    []Plan
	ApplyCalls      int
	IntrospectCalls int
	CompareCalls    int
}

func (fake *FakeProvider[Plan, Schema]) Apply(ctx context.Context, plan Plan) error {
	fake.mu.Lock()
	fake.ApplyCalls++
	fake.AppliedPlans = append(fake.AppliedPlans, plan)
	fake.mu.Unlock()
	if fake.ApplyFunc == nil {
		return nil
	}
	return fake.ApplyFunc(ctx, plan)
}

func (fake *FakeProvider[Plan, Schema]) Introspect(ctx context.Context) (Schema, error) {
	fake.mu.Lock()
	fake.IntrospectCalls++
	fake.mu.Unlock()
	if fake.IntrospectFunc == nil {
		var zero Schema
		return zero, nil
	}
	return fake.IntrospectFunc(ctx)
}

func (fake *FakeProvider[Plan, Schema]) Compare(expected, actual Schema) []Difference {
	fake.mu.Lock()
	fake.CompareCalls++
	fake.mu.Unlock()
	if fake.CompareFunc == nil {
		return nil
	}
	return fake.CompareFunc(expected, actual)
}
