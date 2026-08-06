package extension

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestNormalizeComputedAndCustomExtensionsIntoContractOnly(t *testing.T) {
	compilation := extensionFixture()
	modelID := ir.ModelID("user")
	cacheKey := ir.AttachedMethodIR{PackagePath: "example/social", Name: "GreetingCacheKey"}
	computed := []ComputedDeclaration{{
		ModelID: modelID,
		Field: ir.ComputedFieldContractIR{
			ExtensionID: "user-greeting", Name: "greeting",
			Result:    ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", Nullable: false},
			Arguments: []ir.GraphQLArgumentContractIR{{Name: "prefix", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", Nullable: false}}},
			Requires:  []ir.FieldID{"user-name"},
			Resolver:  ir.AttachedMethodIR{ModelID: &modelID, PackagePath: "example/social", Name: "LoadGreetings", Kind: "computedBatch"},
			Batch: &ir.ComputedBatchContractIR{
				KeyField: "user-id", Loader: ir.AttachedMethodIR{PackagePath: "example/social", Name: "LoadGreetings"}, CacheKey: &cacheKey, MaxBatchSize: 128,
			},
		},
	}}
	custom := []CustomOperationDeclaration{{Operation: ir.CustomOperationContractIR{
		ExtensionID: "search-users", Operation: ir.CustomOperationQuery, Name: "searchUsers",
		Arguments: []ir.GraphQLArgumentContractIR{{Name: "where", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypePredicate, Name: "User", Nullable: true}}},
		Result:    ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Nullable: false, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: "User", Nullable: false}},
		Resolver:  ir.AttachedMethodIR{PackagePath: "example/social", Name: "SearchUsers", Kind: "customQuery"}, Capability: ir.CustomOperationCallerOnly,
	}}}
	modelBefore := compilation.Model
	if diagnostics := Normalize(&compilation, computed, custom); hasErrors(diagnostics) {
		t.Fatalf("Normalize diagnostics = %#v", diagnostics)
	}
	if !reflect.DeepEqual(compilation.Model, modelBefore) {
		t.Fatal("extension normalization changed ModelIR")
	}
	if got := compilation.Contract.Models[0].Computed; len(got) != 1 || got[0].Name != "greeting" || got[0].Batch == nil {
		t.Fatalf("computed contract = %#v", got)
	}
	if got := compilation.Contract.CustomOperations; len(got) != 1 || got[0].Capability != ir.CustomOperationCallerOnly {
		t.Fatalf("custom contract = %#v", got)
	}
}

func TestNormalizeExtensionsRejectsCapabilitiesTypesDependenciesAndCollisionsAtomically(t *testing.T) {
	tests := []struct {
		name     string
		computed []ComputedDeclaration
		custom   []CustomOperationDeclaration
		code     string
	}{
		{
			name: "private capability", code: "P5_CUSTOM_CALLER_ONLY",
			custom: []CustomOperationDeclaration{{Operation: validCustom("unsafe", "unsafe", ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"})}},
		},
		{
			name: "raw SQL shaped type", code: "P5_EXTENSION_TYPE",
			custom: []CustomOperationDeclaration{{Operation: func() ir.CustomOperationContractIR {
				value := validCustom("raw", "raw", ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "RawSQL"})
				value.Capability = ir.CustomOperationCallerOnly
				return value
			}()}},
		},
		{
			name: "foreign dependency", code: "P5_COMPUTED_DEPENDENCY",
			computed: []ComputedDeclaration{{ModelID: "user", Field: validComputed("foreign", "foreign", []ir.FieldID{"post-title"})}},
		},
		{
			name: "stored field collision", code: "P5_COMPUTED_COLLISION",
			computed: []ComputedDeclaration{{ModelID: "user", Field: validComputed("name-computed", "name", []ir.FieldID{"user-name"})}},
		},
		{
			name: "generated root collision", code: "P5_CUSTOM_ROOT_COLLISION",
			custom: []CustomOperationDeclaration{{Operation: func() ir.CustomOperationContractIR {
				value := validCustom("users-custom", "users", ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"})
				value.Capability = ir.CustomOperationCallerOnly
				return value
			}()}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compilation := extensionFixture()
			before := compilation
			diagnostics := Normalize(&compilation, test.computed, test.custom)
			if !diagnosticCode(diagnostics, test.code) {
				t.Fatalf("diagnostics = %#v, want %s", diagnostics, test.code)
			}
			if !reflect.DeepEqual(compilation, before) {
				t.Fatal("failed normalization partially changed compilation")
			}
		})
	}
}

func extensionFixture() ir.CompilationIR {
	user := ir.ModelDeclIR{ID: "user", Go: ir.GoNamedTypeIR{PackagePath: "example/social", Name: "User"}, Fields: []ir.FieldIR{
		{ID: "user-id", Kind: ir.FieldScalar}, {ID: "user-name", Kind: ir.FieldScalar},
	}}
	post := ir.ModelDeclIR{ID: "post", Go: ir.GoNamedTypeIR{PackagePath: "example/social", Name: "Post"}, Fields: []ir.FieldIR{{ID: "post-title", Kind: ir.FieldScalar}}}
	return ir.CompilationIR{
		Model: ir.ModelIR{Models: []ir.ModelDeclIR{user, post}},
		Contract: ir.ContractIR{
			Models: []ir.ModelContractIR{
				{ModelID: "user", GraphQLName: "User", GraphQLPlural: "Users", Exposed: true, Operations: []ir.Operation{ir.OperationFindMany}, Roots: ir.GraphQLRootNamesIR{FindMany: "users"}, Fields: []ir.FieldContractIR{{FieldID: "user-id", GraphQLName: "id"}, {FieldID: "user-name", GraphQLName: "name"}}},
				{ModelID: "post", GraphQLName: "Post", GraphQLPlural: "Posts", Exposed: true, Fields: []ir.FieldContractIR{{FieldID: "post-title", GraphQLName: "title"}}},
			},
		},
	}
}

func validComputed(id ir.ExtensionID, name string, dependencies []ir.FieldID) ir.ComputedFieldContractIR {
	modelID := ir.ModelID("user")
	return ir.ComputedFieldContractIR{
		ExtensionID: id, Name: name, Result: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}, Requires: dependencies,
		Resolver: ir.AttachedMethodIR{ModelID: &modelID, Receiver: ir.GoNamedTypeIR{PackagePath: "example/social", Name: "User"}, Name: "Resolve"},
	}
}

func validCustom(id ir.ExtensionID, name string, result ir.GraphQLTypeIR) ir.CustomOperationContractIR {
	return ir.CustomOperationContractIR{
		ExtensionID: id, Operation: ir.CustomOperationQuery, Name: name, Result: result,
		Resolver: ir.AttachedMethodIR{PackagePath: "example/social", Name: "Resolve"}, Capability: ir.CustomOperationCapability("system"),
	}
}

func diagnosticCode(diagnostics []ir.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
