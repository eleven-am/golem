package operation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

func compileSource(t *testing.T, compiler *Compiler, source string, variables map[string]any) error {
	t.Helper()
	document, err := parser.ParseQuery(&ast.Source{Name: "errors.graphql", Input: source})
	if err != nil {
		t.Fatal(err)
	}
	_, err = compiler.Compile(document, document.Operations[0], variables)
	return err
}

type expectedRefusal struct {
	name, source string
	variables    map[string]any
	kind         compileerr.Kind
	message      string
	root         string
}

func assertRefusals(t *testing.T, compiler *Compiler, tests []expectedRefusal) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := compileSource(t, compiler, test.source, test.variables)
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Kind() != test.kind || typed.Message() != test.message {
				t.Fatalf("error=%v want kind %d message %q", err, test.kind, test.message)
			}
			root, rooted := compileerr.RootOf(err)
			if root != test.root || rooted != (test.root != "") {
				t.Fatalf("root=%q/%t want %q", root, rooted, test.root)
			}
			if typed.Internal() && typed.Message() != "internal server error" {
				t.Fatalf("an Invariant leaks %q", typed.Message())
			}
		})
	}
}

func TestQueryCompileRefusalsAreTypedAndRootedAtTheResponseName(t *testing.T) {
	compilation := social(t)
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	many := post.Roots.FindMany
	assertRefusals(t, compiler, []expectedRefusal{
		{name: "every-root-skipped", source: `{ ` + many + ` @skip(if: true) { id } }`, kind: compileerr.KindEmpty, message: "Selection must not be empty"},
		{name: "root-fragment-missing", source: `{ ...Nope }`, kind: compileerr.KindFragmentInvalid, message: "Fragment spreads in this operation cannot be resolved"},
		{name: "root-fragment-cycle", source: `{ ...A } fragment A on Query { ...B } fragment B on Query { ...A }`, kind: compileerr.KindFragmentInvalid, message: "Fragment spreads in this operation form a cycle"},
		{name: "root-merge-conflict", source: `{ same: ` + many + `(take: 1) { id } same: ` + many + `(take: 2) { id } }`, kind: compileerr.KindFragmentInvalid, message: "Fields with the same response name cannot be merged"},
		{name: "root-directive-without-if", source: `{ ` + many + ` @skip { id } }`, kind: compileerr.KindDirectiveInvalid, message: "Directive @skip requires a Boolean if"},
		{name: "root-directive-not-boolean", source: `{ ` + many + ` @include(if: "yes") { id } }`, kind: compileerr.KindDirectiveInvalid, message: "Directive @include requires a Boolean if"},
		{name: "argument-literal-out-of-range", source: `{ list: ` + many + `(take: 99999999999999999999) { id } }`, kind: compileerr.KindInvalidValue, message: "Argument take on " + many + " has the wrong kind of value", root: "list"},
		{name: "argument-take-zero", source: `{ list: ` + many + `(take: 0) { id } }`, kind: compileerr.KindIntegerRange, message: "Argument take on Post must not be 0", root: "list"},
		{name: "selection-shape", source: `{ list: ` + many + ` { id { x } } }`, kind: compileerr.KindSelectionShape, message: "Field id on Post must not have a selection set", root: "list"},
		{name: "unknown-root-is-an-invariant", source: `{ ghost { id } }`, kind: compileerr.KindInvariant, message: "internal server error", root: "ghost"},
	})
}

func TestCompileInputGuardsAreInvariants(t *testing.T) {
	compiler, err := New(social(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"nil-document": func() error { _, err := compiler.Compile(nil, nil, nil); return err },
		"unsupported-operation": func() error {
			document, parseErr := parser.ParseQuery(&ast.Source{Name: "errors.graphql", Input: `{ __typename }`})
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			_, err := compiler.Compile(document, &ast.OperationDefinition{Operation: ast.Operation("weird")}, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var typed *compileerr.Error
			if err := call(); !errors.As(err, &typed) || typed.Kind() != compileerr.KindInvariant {
				t.Fatalf("error=%v want an Invariant", err)
			}
		})
	}
}

func TestMutationCompileRefusalsAreRootedAndWriteSideRefusalsAreTyped(t *testing.T) {
	compilation := social(t)
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	assertRefusals(t, compiler, []expectedRefusal{
		{name: "every-root-skipped", source: `mutation { __typename @skip(if: true) }`, kind: compileerr.KindEmpty, message: "Selection must not be empty"},
		{name: "batch-payload-without-selection", source: `mutation { moved: ` + post.Roots.UpdateMany + `(where: {all: true}, data: {}) }`, kind: compileerr.KindEmpty, message: "Selection must not be empty", root: "moved"},
		{name: "batch-payload-field-with-selection", source: `mutation { moved: ` + post.Roots.UpdateMany + `(where: {all: true}, data: {}) { count { x } } }`, kind: compileerr.KindSelectionShape, message: "Field count must not have a selection set", root: "moved"},
		{name: "batch-payload-unknown-field", source: `mutation { moved: ` + post.Roots.UpdateMany + `(where: {all: true}, data: {}) { nope } }`, kind: compileerr.KindInvariant, message: "internal server error", root: "moved"},
		{name: "write-side-unknown-input-field", source: `mutation { made: ` + post.Roots.Create + `(data: {nope: 1}) { id } }`, kind: compileerr.KindFieldUnavailable, message: "Input data.nope on Post is not available for this operation", root: "made"},
	})
}

func TestCustomCompileRefusalsAreTypedAndInvariantsAreInternal(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	assertRefusals(t, compiler, []expectedRefusal{
		{name: "required-argument-missing", source: `{ found: searchUsers { id } }`, kind: compileerr.KindRequired, message: "Argument where on searchUsers is required", root: "found"},
		{name: "unknown-argument", source: `{ found: searchUsers(where: {all: true}, nope: 1) { id } }`, kind: compileerr.KindFieldUnavailable, message: "Argument nope on searchUsers is not available for this operation", root: "found"},
		{name: "predicate-empty", source: `{ found: searchUsers(where: {}) { id } }`, kind: compileerr.KindEmpty, message: "Argument where on User must not be empty", root: "found"},
		{name: "predicate-null-field", source: `{ found: searchUsers(where: {name: null}) { id } }`, kind: compileerr.KindNullNotAllowed, message: "Input where.name on User cannot be null", root: "found"},
		{name: "result-without-selection", source: `{ found: searchUsers(where: {all: true}) }`, kind: compileerr.KindSelectionShape, message: "Field searchUsers requires a selection set", root: "found"},
		{name: "result-selection-unknown-field", source: `{ found: searchUsers(where: {all: true}) { nope } }`, kind: compileerr.KindFieldUnavailable, message: "Field nope on User is not available for this operation", root: "found"},
		{name: "scalar-argument-wrong-kind", source: `mutation { imported: importUsers(data: [], metadata: {}, patch: {}) }`, kind: compileerr.KindInvalidValue, message: "Argument data on User has the wrong kind of value", root: "imported"},
	})
	t.Run("selection-on-a-scalar-result", func(t *testing.T) {
		scalar := compilation
		scalar.Contract.CustomOperations = append([]compilerir.CustomOperationContractIR(nil), compilation.Contract.CustomOperations...)
		for index := range scalar.Contract.CustomOperations {
			if scalar.Contract.CustomOperations[index].Name == "searchUsers" {
				scalar.Contract.CustomOperations[index].Result = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"}
			}
		}
		scalarCompiler, err := New(scalar, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		assertRefusals(t, scalarCompiler, []expectedRefusal{
			{name: "scalar-with-selection", source: `{ found: searchUsers(where: {all: true}) { id } }`, kind: compileerr.KindSelectionShape, message: "Field searchUsers must not have a selection set", root: "found"},
		})
	})
	invariants := []struct {
		name   string
		mutate func(*Compiler)
		source string
		cause  string
	}{
		{"predicate-model-absent", func(c *Compiler) {
			c.compilation.Contract.CustomOperations[1].Arguments[0].Type.Name = "Ghost"
		}, `{ found: searchUsers(where: {all: true}) { id } }`, "predicate model Ghost is absent"},
	}
	for _, test := range invariants {
		t.Run(test.name, func(t *testing.T) {
			mutated := compilation
			mutated.Contract.CustomOperations = append([]compilerir.CustomOperationContractIR(nil), compilation.Contract.CustomOperations...)
			for index := range mutated.Contract.CustomOperations {
				mutated.Contract.CustomOperations[index].Arguments = append([]compilerir.GraphQLArgumentContractIR(nil), mutated.Contract.CustomOperations[index].Arguments...)
			}
			copied := *compiler
			copied.compilation = mutated
			test.mutate(&copied)
			err := compileSource(t, &copied, test.source, nil)
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Kind() != compileerr.KindInvariant || !strings.Contains(err.Error(), test.cause) {
				t.Fatalf("error=%v want an Invariant containing %q", err, test.cause)
			}
		})
	}
}

func TestStoredJSONThatBreaksTheScalarBoundIsAnInvariantNotACallerFault(t *testing.T) {
	deep := []byte(strings.Repeat("[", 40) + strings.Repeat("]", 40))
	value, err := encodedJSON(deep)
	var typed *compileerr.Error
	if value != nil || !errors.As(err, &typed) || typed.Kind() != compileerr.KindInvariant || !typed.Internal() {
		t.Fatalf("value=%v error=%v want an Invariant", value, err)
	}
	if ok, err := encodedJSON([]byte(`{"a":[1,2]}`)); err != nil || ok == nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
}
