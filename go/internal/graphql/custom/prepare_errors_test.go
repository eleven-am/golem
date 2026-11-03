package custom

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
)

func TestPrepareRefusalsAreTypedAndInvariantsAreInternal(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	registry, err := New(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	typedCases := []struct {
		name      string
		arguments map[string]any
		kind      compileerr.Kind
		message   string
		argument  string
	}{
		{"unknown-argument", map[string]any{"nope": 1}, compileerr.KindFieldUnavailable, "Argument nope on importUsers is not available for this operation", "nope"},
		{"required-argument-missing", map[string]any{}, compileerr.KindRequired, "Argument data on importUsers is required", "data"},
	}
	for _, test := range typedCases {
		t.Run(test.name, func(t *testing.T) {
			_, err := registry.Prepare(compilerir.CustomOperationMutation, "importUsers", test.arguments)
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Kind() != test.kind || typed.Message() != test.message || typed.Argument() != test.argument || typed.Internal() {
				t.Fatalf("error=%v want %q at %q", err, test.message, test.argument)
			}
		})
	}
	invariants := []struct {
		name  string
		call  func() error
		cause string
	}{
		{"absent-registry", func() error {
			_, err := (*Registry)(nil).Prepare(compilerir.CustomOperationQuery, "searchUsers", nil)
			return err
		}, "custom registry is absent"},
		{"undeclared-root", func() error {
			_, err := registry.Prepare(compilerir.CustomOperationQuery, "ghost", nil)
			return err
		}, `"ghost" is not statically declared`},
		{"value-the-compiler-would-have-coerced", func() error {
			_, err := registry.Prepare(compilerir.CustomOperationQuery, "searchUsers", map[string]any{"where": "not a frozen predicate"})
			return err
		}, "value is not a User predicate"},
	}
	for _, test := range invariants {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Kind() != compileerr.KindInvariant || !typed.Internal() || !strings.Contains(err.Error(), test.cause) {
				t.Fatalf("error=%v want an Invariant containing %q", err, test.cause)
			}
		})
	}
}
