package operation

import (
	"errors"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func finalizePageRoot(t *testing.T, item compilerir.GraphQLTypeIR, query string) (*Compiler, CustomRoot) {
	t.Helper()
	compilation := objectTypesCompilation(t)
	compilation.Contract.DataIncarnation = &compilerir.DataIncarnationContractIR{Kind: "provider", Version: 1}
	page := compilerir.CustomPaginationIR{DefaultTake: 2, MaxTake: 2, MaxCandidates: 4, Revision: "finalize-v1", Item: item}
	compilation.Contract.GraphQLTypes = append(compilation.Contract.GraphQLTypes, compilerir.GraphQLPageDefinition("FinalizedPage", page))
	compilation.Contract.CustomOperations = []compilerir.CustomOperationContractIR{{ExtensionID: "finalize-page", Operation: compilerir.CustomOperationQuery, Name: "pages", Capability: compilerir.CustomOperationCallerOnly, Resolver: compilerir.AttachedMethodIR{PackagePath: compilation.Model.Schema.PackagePath, Name: "Pages", Kind: "customquery"}, Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "FinalizedPage"}, Pagination: &page, Arguments: []compilerir.GraphQLArgumentContractIR{{Name: "take", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int", Nullable: true}}, {Name: "after", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String", Nullable: true}}}}}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	parsed, failures := gqlparser.LoadQuery(schema, query)
	if len(failures) != 0 {
		t.Fatal(failures)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(parsed, parsed.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	return compiler, compiled.Custom[0]
}

func TestFinalizePageValueOwnsEncodedNextAliasesAndTerminalNull(t *testing.T) {
	compiler, root := finalizePageRoot(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}, `{ pages { values: items token: next again: next } }`)
	for _, token := range []string{"opaque-token", ""} {
		original := map[string]any{"values": []any{int32(1)}, "token": nil, "again": nil}
		calls := 0
		value, err := compiler.FinalizePageValue(root, original, func() (string, error) { calls++; return token, nil })
		if err != nil {
			t.Fatal(err)
		}
		var expected any
		if token != "" {
			expected = token
		}
		object := value.(map[string]any)
		if calls != 1 || object["token"] != expected || object["again"] != expected || original["token"] != nil || original["again"] != nil {
			t.Fatalf("aliases mutated/lost: value=%#v original=%#v calls=%d", value, original, calls)
		}
		if _, present := object["next"]; present {
			t.Fatal("canonical next introduced beside response aliases")
		}
	}
	compiler, root = finalizePageRoot(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}, `{ pages { items } }`)
	value, err := compiler.FinalizePageValue(root, map[string]any{"items": []any{}}, func() (string, error) { return "not-selected", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, present := value.(map[string]any)["next"]; present {
		t.Fatal("unselected next became public")
	}
}

func TestFinalizePageValueRejectsInvalidWrapperBeforeMinting(t *testing.T) {
	compiler, root := finalizePageRoot(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}, `{ pages { items token: next } }`)
	for _, value := range []any{nil, []any{}, map[string]any{"items": []any{}}, map[string]any{"items": []any{}, "token": "already-filled"}} {
		calls := 0
		if _, err := compiler.FinalizePageValue(root, value, func() (string, error) { calls++; return "must-not-mint", nil }); err == nil || calls != 0 {
			t.Fatalf("invalid encoded wrapper minted token: %#v err=%v calls=%d", value, err, calls)
		}
	}
	wrong := root
	wrong.Name = "another-page"
	if _, err := compiler.FinalizePageValue(wrong, map[string]any{"items": []any{}, "token": nil}, func() (string, error) { t.Fatal("unregistered page minted token"); return "", nil }); err == nil {
		t.Fatal("unregistered page accepted")
	}
	failure := errors.New("expired")
	if value, err := compiler.FinalizePageValue(root, map[string]any{"items": []any{}, "token": nil}, func() (string, error) { return "", failure }); value != nil || !errors.Is(err, failure) {
		t.Fatalf("finalizer failure became a value: %#v %v", value, err)
	}
}

func TestPreparePageValuePrunesOnlyDirectModelItemsPerOccurrence(t *testing.T) {
	compiler, root := finalizePageRoot(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "User"}, `{ pages { a: items { id } b: items { name } next } }`)
	user := contractNamed(t, compiler.compilation.Contract, "User")
	model, err := publicModelID(user.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := golem.RuntimeModelReadRow(model)
	if err != nil {
		t.Fatal(err)
	}
	original := hydratedObject{fields: map[string]any{"a": []any{nil, row}, "b": []any{row, nil, nil}, "next": nil}}
	value, err := compiler.PreparePageValue(root, original)
	if err != nil {
		t.Fatal(err)
	}
	prepared := value.(hydratedObject)
	if !reflect.DeepEqual(prepared.fields["a"], []any{row}) || !reflect.DeepEqual(prepared.fields["b"], []any{row}) || len(original.fields["a"].([]any)) != 2 || len(original.fields["b"].([]any)) != 3 {
		t.Fatalf("model pruning lost ownership/occurrences: %#v", prepared)
	}
	if original.fields["next"] != nil || prepared.fields["next"] != nil {
		t.Fatal("prepare minted a continuation")
	}
	compiler, root = finalizePageRoot(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}, `{ pages { items next } }`)
	ordinary := map[string]any{"items": []any{nil}, "next": nil}
	value, err = compiler.PreparePageValue(root, ordinary)
	if err != nil || !reflect.DeepEqual(value, ordinary) {
		t.Fatal("page preparation reinterpreted scalar nulls instead of leaving validation to encoder")
	}
}
