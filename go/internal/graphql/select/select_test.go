package selectset

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

func TestGraphQLSelectionCompilerNormalizesFragmentsDirectivesAndCompatibleMerges(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	schemaDocument, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: schemaDocument.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, errors := gqlparser.LoadQuery(schema, `query Feed($withOldest: Boolean!) {
  post(where: { ID: "00000000-0000-0000-0000-000000000001" }) {
    id
    ...PostFields
    newest: comments(take: 5) { ...CommentFields }
    oldest: comments(take: 5) @include(if: $withOldest) { id body }
    ignored: comments @skip(if: true) { id }
    _count { comments }
  }
}
fragment CommentFields on Comment { id body __typename }
fragment PostFields on Post { id }
`)
	if len(errors) != 0 {
		t.Fatalf("query errors = %v", errors)
	}
	post := modelByName(t, compiled.Compilation.Contract, "Post")
	root := query.Operations.ForName("Feed").SelectionSet[0].(*ast.Field)
	result, err := Compile(Request{Compilation: *compiled.Compilation, Model: post, Selections: root.SelectionSet, Fragments: query.Fragments, Variables: map[string]any{"withOldest": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selections) != 4 {
		t.Fatalf("P3 selections = %d, want scalar + two relation aliases + count", len(result.Selections))
	}
	if len(result.Slots) != 4 || result.Slots[1].ResponseName != "newest" || result.Slots[2].ResponseName != "oldest" || result.Slots[3].ResponseName != "_count" {
		t.Fatalf("slots = %#v", result.Slots)
	}
	if result.Slots[1].Occurrence == 0 || result.Slots[2].Occurrence == 0 || result.Slots[1].Occurrence == result.Slots[2].Occurrence {
		t.Fatalf("relation occurrences = %#v", result.Slots)
	}
	request, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyModel(post), Projection: readir.ProjectionSelect, Selection: result.Selections})
	if err != nil {
		t.Fatalf("occurrence-aware P3 request rejected: %v", err)
	}
	if len(request.Selection()) != 4 {
		t.Fatalf("request selections = %d", len(request.Selection()))
	}
}

func TestGraphQLSelectionCompilerRejectsCyclesConflictsAndLimitOverflowBeforeSQL(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	post := modelByName(t, compiled.Compilation.Contract, "Post")
	tests := []struct {
		name       string
		source     string
		maxDepth   int
		maxFields  int
		maxAliases int
		code       string
	}{
		{name: "fragment-cycle", source: `query { ...A } fragment A on Post { id ...B } fragment B on Post { ...A }`, code: "P5_SELECT_FRAGMENT"},
		{name: "response-conflict", source: `{ same: id same: title }`, code: "P5_SELECT_MERGE"},
		{name: "depth", source: `{ comments { replies { id } } }`, maxDepth: 2, code: "P5_SELECT_LIMIT"},
		{name: "fields", source: `{ id title }`, maxFields: 1, code: "P5_SELECT_LIMIT"},
		{name: "aliases", source: `{ first: id second: title }`, maxAliases: 1, code: "P5_SELECT_LIMIT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := parseQuery(t, test.source)
			_, err := Compile(Request{
				Compilation: *compiled.Compilation,
				Model:       post,
				Selections:  document.Operations[0].SelectionSet,
				Fragments:   document.Fragments,
				MaxDepth:    test.maxDepth,
				MaxFields:   test.maxFields,
				MaxAliases:  test.maxAliases,
			})
			if err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestComputedSelectionsInjectOnlyMaskedPublicDependenciesAndCanonicalBatchArguments(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	user := modelByName(t, compilation.Contract, "User")
	contract := contractForModel(t, &compilation, user)
	for index := range contract.Computed {
		if contract.Computed[index].Name == "batchGreeting" {
			contract.Computed[index].Arguments = append(contract.Computed[index].Arguments,
				compilerir.GraphQLArgumentContractIR{Name: "count", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}},
			)
		}
	}
	document := parseQuery(t, `query Batch($prefix: String!) {
  first: batchGreeting(count: 2, prefix: $prefix)
  second: batchGreeting(prefix: "hello", count: 2)
  greeting(prefix: "hello")
  ignored: greeting @skip(if: true)
}`)
	result, err := Compile(Request{Compilation: compilation, Model: user, Selections: document.Operations[0].SelectionSet, Variables: map[string]any{"prefix": "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Slots) != 3 || len(result.Selections) != 2 {
		t.Fatalf("slots=%#v selections=%#v", result.Slots, result.Selections)
	}
	for _, slot := range result.Slots {
		if slot.Kind != SlotComputed || slot.Computed == nil {
			t.Fatalf("dependency escaped as a response slot: %#v", result.Slots)
		}
		for _, dependency := range slot.Computed.Dependencies {
			if dependency.Kind != graphqlextension.DependencyMaskedScalar || dependency.Occurrence != 0 {
				t.Fatalf("computed dependency is not masked public scalar access: %#v", dependency)
			}
		}
	}
	if result.Slots[0].Computed.CanonicalArguments != result.Slots[1].Computed.CanonicalArguments || result.Slots[0].Computed.CanonicalArguments != `{"count":2,"prefix":"hello"}` {
		t.Fatalf("canonical batch argument keys = %q / %q", result.Slots[0].Computed.CanonicalArguments, result.Slots[1].Computed.CanonicalArguments)
	}
	if len(result.Slots[0].Computed.Dependencies) != 2 {
		t.Fatalf("batch key was not injected with declared dependencies: %#v", result.Slots[0].Computed.Dependencies)
	}
	if len(result.Slots[0].Computed.Arguments) != 2 || result.Slots[0].Computed.Arguments[0].Name != "prefix" || result.Slots[0].Computed.Arguments[1].Value != int32(2) {
		t.Fatalf("typed arguments = %#v", result.Slots[0].Computed.Arguments)
	}
	stable := StableSlots(result.Slots)
	stable[0].Computed.Arguments[0].Canonical[0] = 'x'
	if result.Slots[0].Computed.Arguments[0].Canonical[0] == 'x' {
		t.Fatal("stable slots retained mutable canonical argument bytes")
	}
}

func TestComputedRelationDependencyUsesWithheldMaskedOccurrence(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	post := modelByName(t, compilation.Contract, "Post")
	contract := contractForModel(t, &compilation, post)
	model := modelForID(t, compilation.Model, post)
	var relation compilerir.FieldIR
	for _, field := range model.Fields {
		if field.Kind == compilerir.FieldRelation {
			relation = field
			break
		}
	}
	if relation.ID == "" {
		t.Fatal("fixture has no relation")
	}
	contract.Computed = append(contract.Computed, compilerir.ComputedFieldContractIR{
		ExtensionID: "computed-summary", Name: "summary",
		Result:   compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"},
		Requires: []compilerir.FieldID{relation.ID},
	})
	document := parseQuery(t, `{ summary }`)
	result, err := Compile(Request{Compilation: compilation, Model: post, Selections: document.Operations[0].SelectionSet})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Slots) != 1 || result.Slots[0].Kind != SlotComputed || len(result.Selections) != 1 {
		t.Fatalf("result=%#v", result)
	}
	dependency := result.Slots[0].Computed.Dependencies[0]
	if dependency.Kind != graphqlextension.DependencyMaskedRelation || dependency.FieldID != relation.ID || dependency.Occurrence == 0 {
		t.Fatalf("relation dependency=%#v", dependency)
	}
	selection := result.Selections[0]
	child, ok := selection.Request()
	if !ok || selection.OccurrenceID() != dependency.Occurrence || child.ProjectionMode() != readir.ProjectionDefault {
		t.Fatalf("withheld relation projection=%#v child=%#v", selection, child)
	}
}

func TestComputedModelResultCarriesTypedOutputProjectionSeparately(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	user := modelByName(t, compilation.Contract, "User")
	contract := contractForModel(t, &compilation, user)
	model := modelForID(t, compilation.Model, user)
	contract.Computed = append(contract.Computed, compilerir.ComputedFieldContractIR{
		ExtensionID: "computed-peer", Name: "peer",
		Result:   compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "User", Nullable: true},
		Requires: []compilerir.FieldID{model.Fields[0].ID},
	})
	result, err := Compile(Request{Compilation: compilation, Model: user, Selections: parseQuery(t, `{ peer { name } }`).Operations[0].SelectionSet})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selections) != 1 || len(result.Slots) != 1 || len(result.Slots[0].Computed.Projection) != 1 || len(result.Slots[0].Children) != 1 {
		t.Fatalf("computed model projection = %#v", result)
	}
	if result.Slots[0].Computed.Result.Kind != compilerir.GraphQLTypeModel || result.Slots[0].Children[0].ResponseName != "name" {
		t.Fatalf("typed computed output = %#v", result.Slots[0])
	}
}

func TestComputedArgumentsAndLimitsRefuseBeforeRequestBinding(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	user := modelByName(t, compilation.Contract, "User")
	tests := []struct {
		name  string
		query string
		code  string
	}{
		{name: "missing", query: `{ greeting }`, code: "requires argument"},
		{name: "unknown", query: `{ greeting(prefix: "x", extra: 1) }`, code: "unknown argument"},
		{name: "duplicate", query: `{ greeting(prefix: "x", prefix: "y") }`, code: "repeats argument"},
		{name: "merge", query: `{ same: greeting(prefix: "x") ...F } fragment F on User { same: greeting(prefix: "y") }`, code: "P5_SELECT_MERGE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := parseQuery(t, test.query)
			_, err := Compile(Request{Compilation: compilation, Model: user, Selections: document.Operations[0].SelectionSet, Fragments: document.Fragments})
			if err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("error=%v, want %q", err, test.code)
			}
		})
	}
	document := parseQuery(t, `{ greeting(prefix: "x") batchGreeting(prefix: "x") }`)
	_, err := Compile(Request{Compilation: compilation, Model: user, Selections: document.Operations[0].SelectionSet, MaxFields: 1})
	if err == nil || !strings.Contains(err.Error(), "P5_SELECT_LIMIT") {
		t.Fatalf("selection overflow error=%v", err)
	}
	// The canonical representation itself must remain valid exact JSON.
	result, err := Compile(Request{Compilation: compilation, Model: user, Selections: parseQuery(t, `{ batchGreeting(prefix: "x") }`).Operations[0].SelectionSet})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result.Slots[0].Computed.CanonicalArguments), &decoded); err != nil {
		t.Fatalf("canonical arguments are not JSON: %v", err)
	}
}

func parseQuery(t *testing.T, source string) *ast.QueryDocument {
	t.Helper()
	document, err := parser.ParseQuery(&ast.Source{Name: "test.graphql", Input: source})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func contractForModel(t *testing.T, compilation *compilerir.CompilationIR, model compilerir.ModelID) *compilerir.ModelContractIR {
	t.Helper()
	for index := range compilation.Contract.Models {
		if compilation.Contract.Models[index].ModelID == model {
			return &compilation.Contract.Models[index]
		}
	}
	t.Fatalf("missing contract %s", model)
	return nil
}

func modelForID(t *testing.T, schema compilerir.ModelIR, id compilerir.ModelID) compilerir.ModelDeclIR {
	t.Helper()
	for _, model := range schema.Models {
		if model.ID == id {
			return model
		}
	}
	t.Fatalf("missing model %s", id)
	return compilerir.ModelDeclIR{}
}
func modelByName(t *testing.T, contract compilerir.ContractIR, name string) compilerir.ModelID {
	t.Helper()
	for _, model := range contract.Models {
		if model.GraphQLName == name {
			return model.ModelID
		}
	}
	t.Fatalf("missing model %s", name)
	return ""
}

func policyModel(value compilerir.ModelID) [16]byte {
	var result [16]byte
	// Compiler IDs are canonical 32-character hexadecimal strings.
	for index := 0; index < 16; index++ {
		result[index] = hexByte(value[index*2], value[index*2+1])
	}
	return result
}

func hexByte(left, right byte) byte { return hexNibble(left)<<4 | hexNibble(right) }
func hexNibble(value byte) byte {
	if value >= '0' && value <= '9' {
		return value - '0'
	}
	if value >= 'a' && value <= 'f' {
		return value - 'a' + 10
	}
	return value - 'A' + 10
}
