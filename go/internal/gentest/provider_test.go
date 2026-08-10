package gentest

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestVerifyProviderAppliesIntrospectsAndSortsDifferences(t *testing.T) {
	type plan struct{ Name string }
	type schema struct{ Version int }
	fake := &FakeProvider[plan, schema]{
		IntrospectFunc: func(context.Context) (schema, error) {
			return schema{Version: 2}, nil
		},
		CompareFunc: func(expected, actual schema) []Difference {
			return []Difference{
				{Code: "TYPE", Path: "tables.users.columns.name", Expected: "text", Actual: "blob"},
				{Code: "MISSING", Path: "tables.posts", Expected: "present", Actual: "absent"},
			}
		},
	}

	differences, err := VerifyProvider(context.Background(), fake, plan{Name: "initial"}, schema{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if fake.ApplyCalls != 1 || fake.IntrospectCalls != 1 || fake.CompareCalls != 1 {
		t.Fatalf("calls = apply %d introspect %d compare %d", fake.ApplyCalls, fake.IntrospectCalls, fake.CompareCalls)
	}
	if !slices.Equal(fake.AppliedPlans, []plan{{Name: "initial"}}) {
		t.Fatalf("applied plans = %#v", fake.AppliedPlans)
	}
	if differences[0].Path != "tables.posts" || differences[1].Path != "tables.users.columns.name" {
		t.Fatalf("differences are not sorted: %#v", differences)
	}
}

func TestVerifyProviderStopsAfterApplyFailure(t *testing.T) {
	sentinel := errors.New("apply failed")
	fake := &FakeProvider[string, string]{
		ApplyFunc: func(context.Context, string) error { return sentinel },
	}
	_, err := VerifyProvider(context.Background(), fake, "plan", "schema")
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	if fake.IntrospectCalls != 0 || fake.CompareCalls != 0 {
		t.Fatalf("continued after apply failure: introspect %d compare %d", fake.IntrospectCalls, fake.CompareCalls)
	}
}
