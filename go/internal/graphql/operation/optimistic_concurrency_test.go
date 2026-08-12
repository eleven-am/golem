package operation

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlmutation "github.com/eleven-am/golem/go/internal/graphql/mutation"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestOptimisticConcurrencyGraphQLClaimsFreezeIntoExactRuntimeRequests(t *testing.T) {
	compilation := optimisticConcurrencySocial(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	user := contractNamed(t, compilation.Contract, "User")
	selector := selectorNameForField(t, post, "id")
	userSelector := selectorNameForField(t, user, "id")
	query, queryErrors := gqlparser.LoadQuery(schema, `mutation CAS($id: UUID!, $author: UUID!, $expected: BigInt!, $upsert: PostConcurrencyExpectationInput!) {
  changed: `+post.Roots.Update+`(where: { `+selector+`: $id }, expectedVersion: $expected, data: { title: { set: "changed" } }) { id version }
  chosen: `+post.Roots.Upsert+`(where: { `+selector+`: $id }, expectation: $upsert, create: { title: "made", body: "body", author: { connect: { `+userSelector+`: $author } } }, update: { title: { set: "chosen" } }) { id version }
  removed: `+post.Roots.Delete+`(where: { `+selector+`: $id }, expectedVersion: $expected) { id version }
}`)
	if len(queryErrors) != 0 {
		t.Fatalf("query errors = %v", queryErrors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations.ForName("CAS"), map[string]any{
		"id":       "00000000-0000-0000-0000-000000000011",
		"author":   "00000000-0000-0000-0000-000000000001",
		"expected": int64(7),
		"upsert":   map[string]any{"version": int64(7)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Mutations) != 3 {
		t.Fatalf("mutations = %d", len(compiled.Mutations))
	}
	for _, index := range []int{0, 2} {
		claim, ok := compiled.Mutations[index].Frozen.ExistingVersion()
		if !ok || claim != golem.ExpectVersion(7) {
			t.Fatalf("mutation %s existing claim = %#v/%v", compiled.Mutations[index].ResponseName, claim, ok)
		}
		if _, ok := compiled.Mutations[index].Frozen.ConcurrencyExpectation(); ok {
			t.Fatalf("mutation %s acquired an upsert expectation", compiled.Mutations[index].ResponseName)
		}
	}
	upsert, ok := compiled.Mutations[1].Frozen.ConcurrencyExpectation()
	if !ok || upsert != golem.ExpectExisting(7) {
		t.Fatalf("upsert expectation = %#v/%v", upsert, ok)
	}
	if _, ok := compiled.Mutations[1].Frozen.ExistingVersion(); ok {
		t.Fatal("upsert acquired a scalar existing-version claim")
	}
}

func TestOptimisticConcurrencyGraphQLExpectationOneOfRejectsEveryInvalidShape(t *testing.T) {
	compilation := optimisticConcurrencySocial(t)
	binder, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	valid := map[string]any{
		"where":  map[string]any{selectorNameForField(t, post, "id"): "00000000-0000-0000-0000-000000000011"},
		"create": map[string]any{"title": "made", "body": "body"},
		"update": map[string]any{"title": map[string]any{"set": "changed"}},
	}
	for name, expectation := range map[string]any{
		"omitted":      nil,
		"empty":        map[string]any{},
		"both":         map[string]any{"version": int64(1), "absent": true},
		"false absent": map[string]any{"absent": false},
		"null version": map[string]any{"version": nil},
		"zero version": map[string]any{"version": int64(0)},
		"negative":     map[string]any{"version": int64(-1)},
	} {
		t.Run(name, func(t *testing.T) {
			arguments := make(map[string]any, len(valid)+1)
			for key, value := range valid {
				arguments[key] = value
			}
			if name != "omitted" {
				arguments["expectation"] = expectation
			}
			if _, err := binder.mutation.LowerValues(graphqlmutation.Upsert, post.ModelID, arguments, nil); err == nil {
				t.Fatal("invalid expectation was accepted")
			}
		})
	}
	valid["expectation"] = map[string]any{"absent": true}
	request, err := binder.mutation.LowerValues(graphqlmutation.Upsert, post.ModelID, valid, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectation, ok := request.ConcurrencyExpectation()
	if !ok || expectation != golem.ExpectAbsent() {
		t.Fatalf("absent expectation = %#v/%v", expectation, ok)
	}
	if _, err := binder.mutation.CustomInput(graphqlmutation.CreateInput, post.ModelID, map[string]any{"version": int64(1)}); err == nil {
		t.Fatal("runtime-owned token was accepted through a custom create input")
	}
}

func TestVersionedCustomSelectorBindsWithoutManufacturingDeleteAuthority(t *testing.T) {
	compilation := optimisticConcurrencySocial(t)
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	value, err := compiler.bindCustomValue(compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeSelector, Name: "Post"}, map[string]any{
		selectorNameForField(t, post, "id"): "00000000-0000-0000-0000-000000000011",
	})
	if err != nil {
		t.Fatal(err)
	}
	target, ok := value.(golem.FrozenMutationTarget)
	if !ok || target.Selector().ModelID() != golemModelID(post.ModelID) || len(target.Selector().Fields()) != 1 {
		t.Fatalf("custom selector = %#v", value)
	}
}

func TestOldGraphQLNestedMapCannotMutateVersionedInverseOwner(t *testing.T) {
	compilation := optimisticConcurrencySocial(t)
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	user := contractNamed(t, compilation.Contract, "User")
	post := contractNamed(t, compilation.Contract, "Post")
	_, err = compiler.mutation.LowerValues(graphqlmutation.Update, user.ModelID, map[string]any{
		"where": map[string]any{selectorNameForField(t, user, "id"): "00000000-0000-0000-0000-000000000001"},
		"data": map[string]any{"posts": map[string]any{"connect": []any{
			map[string]any{selectorNameForField(t, post, "id"): "00000000-0000-0000-0000-000000000011"},
		}}},
	}, nil)
	if err == nil {
		t.Fatal("old nested connect map mutated a versioned inverse owner without an expectation")
	}
}

func optimisticConcurrencySocial(t *testing.T) compilerir.CompilationIR {
	t.Helper()
	compilation := social(t)
	versionID := compilerir.FieldID("f0000000000000000000000000000090")
	for modelIndex := range compilation.Model.Models {
		model := &compilation.Model.Models[modelIndex]
		if model.LogicalName != "Post" {
			continue
		}
		model.Fields = append(model.Fields, compilerir.FieldIR{ID: versionID, GoName: "Version", LogicalName: "version", DeclarationOrder: 100, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "version", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}}})
		model.OptimisticConcurrency = &versionID
		for contractIndex := range compilation.Contract.Models {
			contract := &compilation.Contract.Models[contractIndex]
			if contract.ModelID == model.ID {
				contract.OptimisticConcurrency = &versionID
				contract.Fields = append(contract.Fields, compilerir.FieldContractIR{FieldID: versionID, GraphQLName: "version", Modes: []compilerir.FieldMode{compilerir.ModeVisible}})
			}
		}
	}
	return compilation
}
