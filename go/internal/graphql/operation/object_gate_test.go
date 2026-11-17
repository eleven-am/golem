package operation

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestDTOGateRunsForUnselectedModelMember(t *testing.T) {
	compilation := objectTypesCompilation(t)
	for i := range compilation.Contract.GraphQLTypes {
		definition := &compilation.Contract.GraphQLTypes[i]
		if definition.Name != "TransportMetadata" {
			continue
		}
		definition.RowGate = "user"
		definition.Fields = []compilerir.GraphQLObjectFieldIR{
			{Name: "id", GoName: "ID", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"}},
			{Name: "user", GoName: "User", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "User", Nullable: true}},
		}
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, failures := gqlparser.LoadQuery(schema, `{ transportMetadata { id } }`)
	if len(failures) != 0 {
		t.Fatal(failures)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, needed := compiler.CustomObjectHydration(compiled.Custom[0])
	if !needed {
		t.Fatal("unselected gate was ignored")
	}
	user := contractNamed(t, compilation.Contract, "User")
	model, err := publicModelID(user.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	var key golem.FieldID
	for _, field := range user.Fields {
		if field.GraphQLName == "id" {
			key, err = publicFieldID(field.FieldID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	row := identifiedRowForTest(t, model, key, golem.UUID{1})
	prepared, err := plan.Collect(map[string]any{"id": "must not disclose", "user": row})
	if err != nil {
		t.Fatal(err)
	}
	groups := plan.Groups()
	if len(groups) != 1 || groups[0].Len() != 1 {
		t.Fatalf("gate groups=%#v", groups)
	}
	if _, err := compiler.HydrationRequest(groups[0], 0, 1); err != nil {
		t.Fatal(err)
	}
	denied, absent, err := plan.Restore(prepared, []HydratedRows{{}})
	if err != nil || absent != 1 || denied != nil {
		t.Fatalf("denied gate exposed sibling scalar: %#v absent=%d error=%v", denied, absent, err)
	}
	allowed, absent, err := plan.Restore(prepared, []HydratedRows{{golem.RuntimeModelRowIdentity(row).Token(): row}})
	if err != nil || absent != 0 || allowed == nil {
		t.Fatalf("readable gate was discarded: %v", err)
	}
}
