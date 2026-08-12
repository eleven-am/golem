package schema

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestOptimisticConcurrencyGraphQLSchemaIsExactAndUnsafeSurfacesAreAbsent(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	versionID := compilerir.FieldID("f0000000000000000000000000000090")
	for index := range compilation.Model.Models {
		model := &compilation.Model.Models[index]
		if model.LogicalName != "Post" {
			continue
		}
		model.Fields = append(model.Fields, compilerir.FieldIR{
			ID: versionID, GoName: "Version", LogicalName: "version", DeclarationOrder: 100,
			Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "version", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}},
		})
		model.OptimisticConcurrency = &versionID
		for contractIndex := range compilation.Contract.Models {
			contract := &compilation.Contract.Models[contractIndex]
			if contract.ModelID != model.ID {
				continue
			}
			contract.OptimisticConcurrency = &versionID
			contract.Fields = append(contract.Fields, compilerir.FieldContractIR{FieldID: versionID, GraphQLName: "version", Modes: []compilerir.FieldMode{compilerir.ModeVisible}})
		}
	}

	document, err := Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatalf("invalid optimistic-concurrency SDL: %v\n%s", err, document.SDL)
	}
	for inputName, input := range parsed.Types {
		if input == nil || input.Kind != ast.InputObject || (!strings.HasPrefix(inputName, "PostCreate") && !strings.HasPrefix(inputName, "PostUpdate")) {
			continue
		}
		if input.Fields.ForName("version") != nil {
			t.Fatalf("runtime-owned token leaked into %s", inputName)
		}
	}
	if parsed.Types["PostUpdateManyInput"] != nil {
		t.Fatal("versioned PostUpdateManyInput was generated")
	}
	if requireDefinition(t, parsed, "Post").Fields.ForName("version") == nil || requireDefinition(t, parsed, "PostWhereInput").Fields.ForName("version") == nil {
		t.Fatal("ordinary read/filter exposure lost the concurrency token")
	}
	expectation := requireDefinition(t, parsed, "PostConcurrencyExpectationInput")
	requireFieldNames(t, expectation, "version", "absent")
	if expectation.Fields.ForName("version").Type.String() != "BigInt" || expectation.Fields.ForName("absent").Type.String() != "Boolean" {
		t.Fatalf("upsert expectation fields = %#v", expectation.Fields)
	}
	mutation := requireDefinition(t, parsed, "Mutation")
	post := contractForGraphQLName(t, compilation.Contract, "Post")
	update := mutation.Fields.ForName(post.Roots.Update)
	deleteRoot := mutation.Fields.ForName(post.Roots.Delete)
	upsert := mutation.Fields.ForName(post.Roots.Upsert)
	if update == nil || update.Arguments.ForName("expectedVersion") == nil || update.Arguments.ForName("expectedVersion").Type.String() != "BigInt!" {
		t.Fatalf("versioned update root = %#v", update)
	}
	if deleteRoot == nil || deleteRoot.Arguments.ForName("expectedVersion") == nil || deleteRoot.Arguments.ForName("expectedVersion").Type.String() != "BigInt!" {
		t.Fatalf("versioned delete root = %#v", deleteRoot)
	}
	if upsert == nil || upsert.Arguments.ForName("expectation") == nil || upsert.Arguments.ForName("expectation").Type.String() != "PostConcurrencyExpectationInput!" {
		t.Fatalf("versioned upsert root = %#v", upsert)
	}
	if mutation.Fields.ForName(post.Roots.UpdateMany) != nil || mutation.Fields.ForName(post.Roots.DeleteMany) != nil {
		t.Fatal("versioned batch roots were generated")
	}
	if postUpdate := requireDefinition(t, parsed, "PostUpdateInput"); postUpdate.Fields.ForName("author") != nil || postUpdate.Fields.ForName("comments") != nil {
		t.Fatalf("versioned root update retained relation writes: %#v", postUpdate.Fields)
	}
	for _, model := range compilation.Model.Models {
		if model.ID != post.ModelID {
			continue
		}
		for _, field := range model.Fields {
			if field.Relation == nil {
				continue
			}
			fieldContract := contractFieldByIdentity(t, post, field.ID)
			name := "Post" + exported(fieldContract.GraphQLName) + "UpdateRelationInput"
			if parsed.Types[name] != nil {
				t.Fatalf("orphan versioned-root relation helper %s was emitted", name)
			}
		}
	}
	userPosts := requireDefinition(t, parsed, "UserPostsUpdateRelationInput")
	for _, forbidden := range []string{"connect", "connectOrCreate", "disconnect", "set", "update", "updateMany", "upsert", "delete", "deleteMany"} {
		if userPosts.Fields.ForName(forbidden) != nil {
			t.Fatalf("unsafe versioned nested operation %s leaked into UserPostsUpdateRelationInput", forbidden)
		}
	}
}

func contractFieldByIdentity(t *testing.T, contract compilerir.ModelContractIR, field compilerir.FieldID) compilerir.FieldContractIR {
	t.Helper()
	for _, candidate := range contract.Fields {
		if candidate.FieldID == field {
			return candidate
		}
	}
	t.Fatalf("contract field %s is absent", field)
	return compilerir.FieldContractIR{}
}

func TestVersionedInverseRelationWithNoLegalCreateBranchIsOmittedAtomically(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	versionID := compilerir.FieldID("f0000000000000000000000000000092")
	var postID compilerir.ModelID
	for index := range compilation.Model.Models {
		model := &compilation.Model.Models[index]
		if model.LogicalName != "Post" {
			continue
		}
		postID = model.ID
		model.Fields = append(model.Fields, compilerir.FieldIR{ID: versionID, GoName: "Version", LogicalName: "version", DeclarationOrder: 100, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "version", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}}})
		model.OptimisticConcurrency = &versionID
	}
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		if contract.ModelID != postID {
			continue
		}
		contract.OptimisticConcurrency = &versionID
		contract.Fields = append(contract.Fields, compilerir.FieldContractIR{FieldID: versionID, GraphQLName: "version", Modes: []compilerir.FieldMode{compilerir.ModeVisible}})
		for fieldIndex := range contract.Fields {
			if contract.Fields[fieldIndex].FieldID != versionID {
				contract.Fields[fieldIndex].Modes = append(contract.Fields[fieldIndex].Modes, compilerir.ModeReadOnly)
			}
		}
		var operations []compilerir.Operation
		for _, operation := range contract.Operations {
			if operation != compilerir.OperationCreate && operation != compilerir.OperationUpsert {
				operations = append(operations, operation)
			}
		}
		contract.Operations = operations
	}
	document, err := Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatalf("invalid empty-branch SDL: %v\n%s", err, document.SDL)
	}
	if input := requireDefinition(t, parsed, "UserCreateInput"); input.Fields.ForName("posts") != nil {
		t.Fatalf("UserCreateInput retained an uninhabitable posts relation: %#v", input.Fields.ForName("posts"))
	}
	if parsed.Types["UserPostsCreateRelationInput"] != nil {
		t.Fatal("empty UserPostsCreateRelationInput was emitted")
	}
}

func contractForGraphQLName(t *testing.T, contract compilerir.ContractIR, name string) compilerir.ModelContractIR {
	t.Helper()
	for _, model := range contract.Models {
		if model.GraphQLName == name {
			return model
		}
	}
	t.Fatalf("GraphQL contract %s is absent", name)
	return compilerir.ModelContractIR{}
}
