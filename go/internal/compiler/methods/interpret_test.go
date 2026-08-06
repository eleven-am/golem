package methods

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
)

const fixturePackage = "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/basic"
const rejectPackage = "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/reject"
const signaturePackage = "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/signatures"
const extensionPackage = "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/extensions"
const invalidExtensionPackage = "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/extensionsinvalid"

func TestInterpretTypedOverlayAndOptionalMethod(t *testing.T) {
	directory := fixtureDirectory(t, "basic")
	compilation := fixtureCompilation()
	result := Interpret(context.Background(), Config{
		Dir:         moduleDirectory(t),
		Compilation: compilation,
		Packages:    []modelcodegen.PackageSpec{{ImportPath: fixturePackage, PackageName: "basic", Directory: directory}},
	})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
	if len(result.Advanced) != 2 {
		t.Fatalf("advanced models = %d, want 2", len(result.Advanced))
	}
	if len(result.GraphQLModels) != 1 {
		t.Fatalf("GraphQL model patches = %#v", result.GraphQLModels)
	}
	if len(result.GraphQLComputed) != 3 {
		t.Fatalf("computed GraphQL declarations = %#v", result.GraphQLComputed)
	}
	if result.GraphQLComputed[0].Field.Name != "batchGreeting" || result.GraphQLComputed[0].Field.Batch == nil || result.GraphQLComputed[0].Field.Batch.MaxBatchSize != 32 {
		t.Fatalf("batch computed declaration = %#v", result.GraphQLComputed[0])
	}
	if result.GraphQLComputed[1].Field.Batch == nil || result.GraphQLComputed[1].Field.Batch.CacheKey == nil || result.GraphQLComputed[2].Field.Name != "greeting" || !reflect.DeepEqual(result.GraphQLComputed[2].Field.Requires, []ir.FieldID{"13000000000000000000000000000000"}) {
		t.Fatalf("computed declarations = %#v", result.GraphQLComputed)
	}
	graphql := result.GraphQLModels[0]
	if graphql.ModelID != userID || graphql.Plural == nil || *graphql.Plural != "accounts" || graphql.Roots == nil || graphql.Roots.FindOne != "account" || graphql.DefaultPage == nil || *graphql.DefaultPage != 25 || graphql.MaximumPage == nil || *graphql.MaximumPage != 250 {
		t.Fatalf("GraphQL model patch = %#v", graphql)
	}
	if graphql.Operations == nil || len(*graphql.Operations) != 3 || (*graphql.Operations)[2] != ir.OperationCreate {
		t.Fatalf("GraphQL operations = %#v", graphql.Operations)
	}
	user := result.Advanced[0]
	if user.ModelID != userID {
		t.Fatalf("last advanced model = %s, want user", user.ModelID)
	}
	if len(user.Keys) != 2 || user.Keys[0].Kind != ir.KeyPrimary || user.Keys[1].Kind != ir.KeyUnique {
		t.Fatalf("keys = %#v", user.Keys)
	}
	if len(user.Indexes) != 1 || len(user.Indexes[0].Keys) != 3 || user.Indexes[0].Keys[0].Direction != ir.SortDesc || user.Indexes[0].Predicate == nil {
		t.Fatalf("indexes = %#v", user.Indexes)
	}
	cast := user.Indexes[0].Keys[2].Expr
	if cast == nil || cast.Kind != ir.SchemaExprCast || cast.ResultType.Kind != ir.TypeInt64 || cast.Symbol == nil || cast.Symbol.Identity != schemaexpr.CastInt16ToInt64 {
		t.Fatalf("cast index expression = %#v", cast)
	}
	if len(user.Checks) != 1 {
		t.Fatalf("checks = %#v", user.Checks)
	}
	if len(user.Generated) != 1 || user.Generated[0].Generation.Provider != ir.ProviderScopePostgreSQL || user.Generated[0].Generation.Storage != ir.GeneratedStored {
		t.Fatalf("generated = %#v", user.Generated)
	}
	audit := result.Advanced[1]
	if audit.ModelID != auditID || len(audit.Keys)+len(audit.Indexes)+len(audit.Checks)+len(audit.Generated) != 0 {
		t.Fatalf("optional-method model = %#v", audit)
	}
}

func TestInterpretDeterministic(t *testing.T) {
	// Package metadata is intentionally omitted: Interpret discovers it from
	// registered import paths before emitting the clean-checkout overlay.
	config := Config{Dir: moduleDirectory(t), Compilation: fixtureCompilation()}
	first := Interpret(context.Background(), config)
	second := Interpret(context.Background(), config)
	if len(first.Diagnostics) != 0 || len(second.Diagnostics) != 0 {
		t.Fatalf("diagnostics: first=%#v second=%#v", first.Diagnostics, second.Diagnostics)
	}
	if len(first.Advanced) != len(second.Advanced) || first.Advanced[0].Indexes[0].Predicate.CanonicalIdentity != second.Advanced[0].Indexes[0].Predicate.CanonicalIdentity {
		t.Fatalf("non-deterministic results: first=%#v second=%#v", first.Advanced, second.Advanced)
	}
}

func TestInterpretGraphQLModelAndSchemaExtensionsDeterministically(t *testing.T) {
	config := Config{Dir: moduleDirectory(t), Compilation: extensionCompilation(extensionPackage), Packages: []modelcodegen.PackageSpec{{ImportPath: extensionPackage, PackageName: "extensions", Directory: fixtureDirectory(t, "extensions")}}}
	first := Interpret(context.Background(), config)
	second := Interpret(context.Background(), config)
	if len(first.Diagnostics) != 0 || len(second.Diagnostics) != 0 {
		t.Fatalf("diagnostics: first=%#v second=%#v", first.Diagnostics, second.Diagnostics)
	}
	if len(first.GraphQLComputed) != 1 || first.GraphQLComputed[0].Field.Name != "greeting" {
		t.Fatalf("computed = %#v", first.GraphQLComputed)
	}
	if len(first.GraphQLCustom) != 2 || first.GraphQLCustom[0].Operation.Operation != ir.CustomOperationMutation || first.GraphQLCustom[1].Operation.Name != "searchUsers" {
		t.Fatalf("custom = %#v", first.GraphQLCustom)
	}
	if first.GraphQLComputed[0].Field.ExtensionID != second.GraphQLComputed[0].Field.ExtensionID || first.GraphQLCustom[0].Operation.ExtensionID != second.GraphQLCustom[0].Operation.ExtensionID {
		t.Fatalf("extension identities are not deterministic: first=%#v second=%#v", first, second)
	}
	compilation := config.Compilation
	if diagnostics := graphqlextension.Normalize(&compilation, first.GraphQLComputed, first.GraphQLCustom); len(diagnostics) != 0 {
		t.Fatalf("normalization diagnostics = %#v", diagnostics)
	}
	if len(compilation.Contract.Models[0].Computed) != 1 || len(compilation.Contract.CustomOperations) != 2 {
		t.Fatalf("normalized contract = %#v", compilation.Contract)
	}
}

func TestInterpretGraphQLExtensionsRejectsInvalidSignaturesCapabilitiesTypesAndSourceCollision(t *testing.T) {
	config := Config{Dir: moduleDirectory(t), Compilation: extensionCompilation(invalidExtensionPackage), Packages: []modelcodegen.PackageSpec{{ImportPath: invalidExtensionPackage, PackageName: "extensionsinvalid", Directory: fixtureDirectory(t, "extensionsinvalid")}}}
	result := Interpret(context.Background(), config)
	for _, code := range []string{"P5_COMPUTED_SIGNATURE", "P5_COMPUTED_BATCH_SIGNATURE", "P5_CUSTOM_SIGNATURE", "P5_EXTENSION_ARGUMENT_TYPE"} {
		if !containsDiagnostic(result.Diagnostics, code) {
			t.Errorf("missing %s in %#v", code, result.Diagnostics)
		}
	}
	if len(result.GraphQLCustom) != 1 || result.GraphQLCustom[0].Operation.Name != "users" {
		t.Fatalf("valid collision declaration was not retained: %#v", result.GraphQLCustom)
	}
	compilation := config.Compilation
	diagnostics := graphqlextension.Normalize(&compilation, result.GraphQLComputed, result.GraphQLCustom)
	if !containsDiagnostic(diagnostics, "P5_CUSTOM_ROOT_COLLISION") {
		t.Fatalf("missing root collision diagnostic in %#v", diagnostics)
	}
}

func TestInterpretRejectsInvalidReceiverAndResult(t *testing.T) {
	result := Interpret(context.Background(), Config{Dir: moduleDirectory(t), Compilation: signatureCompilation()})
	want := map[string]bool{"P1_METHOD_RECEIVER": false, "P1_METHOD_SIGNATURE": false}
	for _, diagnostic := range result.Diagnostics {
		if _, exists := want[diagnostic.Code]; exists {
			want[diagnostic.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Errorf("missing diagnostic %s in %#v", code, result.Diagnostics)
		}
	}
}

func TestInterpretRejectsHelpersAndForgedTypedValues(t *testing.T) {
	result := Interpret(context.Background(), Config{
		Dir: moduleDirectory(t), Compilation: rejectCompilation(),
		Packages: []modelcodegen.PackageSpec{{ImportPath: rejectPackage, PackageName: "reject", Directory: fixtureDirectory(t, "reject")}},
	})
	want := map[string]bool{"P1_METHOD_OPTION_CALL": false, "P1_METHOD_PROVIDER": false, "P1_METHOD_PROVIDER_NESTING": false, "P1_METHOD_RELATION_ACTION": false, "P1_METHOD_CAST_IDENTITY": false, "P1_METHOD_INDEX_COLUMN": false}
	for _, diagnostic := range result.Diagnostics {
		if _, exists := want[diagnostic.Code]; exists {
			want[diagnostic.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Errorf("missing diagnostic %s in %#v", code, result.Diagnostics)
		}
	}
	if len(result.RelationOptions) != 1 || result.RelationOptions[0].OnUpdate == nil || *result.RelationOptions[0].OnUpdate != ir.ActionCascade {
		t.Fatalf("relation options = %#v", result.RelationOptions)
	}
}

const (
	userID  ir.ModelID = "10000000000000000000000000000000"
	auditID ir.ModelID = "20000000000000000000000000000000"
)

func fixtureCompilation() ir.CompilationIR {
	field := func(id, name string, kind ir.LogicalTypeKind) ir.FieldIR {
		return ir.FieldIR{ID: ir.FieldID(id), GoName: name, LogicalName: name, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: ir.SQLIdentifier(name), Type: ir.LogicalTypeIR{Kind: kind}}}
	}
	return ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{
		{ID: userID, Go: ir.GoNamedTypeIR{PackagePath: fixturePackage, Name: "User"}, LogicalName: "User", Table: ir.TableBindingIR{PhysicalName: "users"}, Fields: []ir.FieldIR{
			field("11000000000000000000000000000000", "ID", ir.TypeInt64),
			field("12000000000000000000000000000000", "Age", ir.TypeInt64),
			field("13000000000000000000000000000000", "Name", ir.TypeString),
			field("14000000000000000000000000000000", "Score", ir.TypeInt64),
			field("15000000000000000000000000000000", "Small", ir.TypeInt16),
		}},
		{ID: auditID, Go: ir.GoNamedTypeIR{PackagePath: fixturePackage, Name: "Audit"}, LogicalName: "Audit", Table: ir.TableBindingIR{PhysicalName: "audits"}, Fields: []ir.FieldIR{
			field("21000000000000000000000000000000", "ID", ir.TypeInt64),
		}},
	}}}
}

func extensionCompilation(packagePath string) ir.CompilationIR {
	const (
		modelID   ir.ModelID = "40000000000000000000000000000000"
		idField   ir.FieldID = "41000000000000000000000000000000"
		nameField ir.FieldID = "42000000000000000000000000000000"
	)
	return ir.CompilationIR{
		Model: ir.ModelIR{
			FormatVersion: ir.ModelFormatVersion,
			Schema:        ir.SchemaIdentityIR{ID: "43000000000000000000000000000000", StableName: "extensions", PackagePath: packagePath},
			Models: []ir.ModelDeclIR{{
				ID: modelID, Go: ir.GoNamedTypeIR{PackagePath: packagePath, Name: "User"}, LogicalName: "User", Table: ir.TableBindingIR{PhysicalName: "users"},
				Fields: []ir.FieldIR{
					{ID: idField, GoName: "ID", LogicalName: "ID", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: "id", Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}},
					{ID: nameField, GoName: "Name", LogicalName: "Name", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: "name", Type: ir.LogicalTypeIR{Kind: ir.TypeString}}},
				},
			}},
		},
		Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{{
			ModelID: modelID, GraphQLName: "User", GraphQLPlural: "Users", Exposed: true,
			Roots: ir.GraphQLRootNamesIR{FindMany: "users"}, Operations: []ir.Operation{ir.OperationFindMany},
			Fields: []ir.FieldContractIR{{FieldID: idField, GraphQLName: "id"}, {FieldID: nameField, GraphQLName: "name"}},
		}}},
	}
}

func containsDiagnostic(diagnostics []ir.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func rejectCompilation() ir.CompilationIR {
	const (
		modelID       ir.ModelID    = "30000000000000000000000000000000"
		idField       ir.FieldID    = "31000000000000000000000000000000"
		relationField ir.FieldID    = "32000000000000000000000000000000"
		relationID    ir.RelationID = "33000000000000000000000000000000"
		smallField    ir.FieldID    = "34000000000000000000000000000000"
	)
	return ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion,
		Models: []ir.ModelDeclIR{{ID: modelID, Go: ir.GoNamedTypeIR{PackagePath: rejectPackage, Name: "User"}, LogicalName: "User", Table: ir.TableBindingIR{PhysicalName: "users"}, Fields: []ir.FieldIR{
			{ID: idField, GoName: "ID", LogicalName: "ID", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: "id", Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}},
			{ID: relationField, GoName: "Parent", LogicalName: "Parent", Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{RelationID: relationID, Role: ir.RelationSource, Kind: ir.RelationBelongsTo}},
			{ID: smallField, GoName: "Small", LogicalName: "Small", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: "small", Type: ir.LogicalTypeIR{Kind: ir.TypeInt16}}},
		}}},
		Relations: []ir.RelationIR{{ID: relationID, Name: "parent", SourceModel: modelID, TargetModel: modelID, SourceField: relationField, LocalFields: []ir.FieldID{idField}, RemoteFields: []ir.FieldID{idField}}},
	}}
}

func signatureCompilation() ir.CompilationIR {
	names := []struct {
		id   ir.ModelID
		name string
	}{
		{"40000000000000000000000000000000", "Pointer"},
		{"50000000000000000000000000000000", "WrongResult"},
		{"60000000000000000000000000000000", "Other"},
	}
	result := ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion}}
	for index, item := range names {
		fieldID := ir.FieldID(string(rune('7'+index)) + "1000000000000000000000000000000")
		result.Model.Models = append(result.Model.Models, ir.ModelDeclIR{ID: item.id, Go: ir.GoNamedTypeIR{PackagePath: signaturePackage, Name: item.name}, LogicalName: item.name, Fields: []ir.FieldIR{{ID: fieldID, GoName: "ID", LogicalName: "ID", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: "id", Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}}}})
	}
	return result
}

func moduleDirectory(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}

func fixtureDirectory(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(moduleDirectory(t), "internal", "compiler", "methods", "testdata", name)
}
