package compile

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/methods"
	"github.com/eleven-am/golem/go/internal/compiler/schema"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestCompleteSocialFixture(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/social", Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %#v", result.Diagnostics)
	}
	if result.Compilation == nil {
		t.Fatal("successful compile returned nil CompilationIR")
	}
	model := result.Compilation.Model
	if len(model.Models) != 6 || len(model.Relations) != 8 {
		t.Fatalf("models=%d relations=%d", len(model.Models), len(model.Relations))
	}
	if len(model.Providers) != 2 || model.Providers[0] != ir.SQLite || model.Providers[1] != ir.PostgreSQL {
		t.Fatalf("providers: %#v", model.Providers)
	}
	assertCompositePrimary(t, model, "Friendship")
	assertCompositePrimary(t, model, "PostTag")
	var recursive bool
	for _, relation := range model.Relations {
		if relation.SourceModel == relation.TargetModel && relation.Name == "ReplyTree" {
			recursive = true
		}
	}
	if !recursive {
		t.Fatal("missing recursive comment relation")
	}
	var advanced, cascade bool
	var postID ir.ModelID
	for _, entry := range model.Models {
		if entry.CanonicalIdentity == "" {
			t.Fatalf("model %s lost canonical identity", entry.Go.Name)
		}
		for _, field := range entry.Fields {
			if field.CanonicalIdentity == "" {
				t.Fatalf("field %s.%s lost canonical identity", entry.Go.Name, field.GoName)
			}
		}
		if entry.LogicalName != "Post" {
			continue
		}
		postID = entry.ID
		var titleID ir.FieldID
		for _, field := range entry.Fields {
			if field.GoName == "Title" {
				titleID = field.ID
			}
			if field.GoName == "Search" && field.Scalar != nil && field.Scalar.Generation != nil && field.Scalar.DatabaseReadOnly {
				expr := field.Scalar.Generation.Expr
				advanced = expr.ResultType.Kind == ir.TypeString && expr.Deterministic && expr.Volatility == ir.SchemaVolatilityImmutable && len(expr.ReferencedFields) == 1
			}
		}
		if len(entry.Checks) != 1 || len(entry.Uniques) == 0 || len(entry.Indexes) < 3 {
			t.Fatalf("advanced Post declarations missing: %#v", entry)
		}
		foundExpression, foundPostgres := false, false
		for _, index := range entry.Indexes {
			if index.PhysicalName == "idx_posts_lower_title" {
				foundExpression = len(index.Keys) == 1 && index.Keys[0].Expr != nil && index.Predicate != nil && len(index.Predicate.ReferencedFields) == 1 && index.Predicate.ReferencedFields[0] == titleID
			}
			foundPostgres = foundPostgres || index.PhysicalName == "idx_posts_title_pg" && index.Provider == ir.ProviderScopePostgreSQL
		}
		if !foundExpression || !foundPostgres || entry.Checks[0].Predicate.CanonicalIdentity == "" || len(entry.Checks[0].Predicate.ReferencedFields) != 1 || entry.Checks[0].Predicate.ReferencedFields[0] != titleID {
			t.Fatal("missing normalized advanced index/check nodes")
		}
	}
	for _, edge := range model.Relations {
		cascade = cascade || edge.SourceModel == postID && edge.Name == "Author" && edge.ForeignKey != nil && edge.ForeignKey.OnDelete == ir.ActionCascade
	}
	if !advanced || !cascade {
		t.Fatalf("advanced generated/action nodes missing: generated=%v cascade=%v", advanced, cascade)
	}

	golden, err := os.ReadFile("testdata/social.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := json.MarshalIndent(struct {
		Models              int            `json:"models"`
		Relations           int            `json:"relations"`
		ModelFingerprint    ir.Fingerprint `json:"modelFingerprint"`
		ContractFingerprint ir.Fingerprint `json:"contractFingerprint"`
	}{len(model.Models), len(model.Relations), result.ModelFingerprint, result.ContractFingerprint}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	summary = append(summary, '\n')
	if string(summary) != string(golden) {
		t.Fatalf("golden mismatch\nwant: %s\n got: %s", golden, summary)
	}
}

func TestGraphQLContractNormalizationMaterializesNamesOperationsLimitsEnumsAndExtensions(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/graphql_extensions", Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	contract := result.Compilation.Contract
	if len(contract.Models) != 1 || len(contract.Models[0].Computed) != 2 {
		t.Fatalf("computed contract = %#v", contract.Models)
	}
	modelContract := contract.Models[0]
	if modelContract.GraphQLName != "User" || modelContract.GraphQLPlural != "Users" || modelContract.Roots.FindOne != "user" || modelContract.Roots.FindMany != "users" || len(modelContract.Operations) != 8 || modelContract.Limits.DefaultPageSize != 50 || modelContract.Limits.MaxPageSize != 500 {
		t.Fatalf("normalized GraphQL names/operations/limits = %#v", modelContract)
	}
	fieldNames := map[string]bool{}
	for _, field := range modelContract.Fields {
		fieldNames[field.GraphQLName] = true
	}
	for _, name := range []string{"id", "name", "status"} {
		if !fieldNames[name] {
			t.Errorf("normalized contract omitted field name %q: %#v", name, modelContract.Fields)
		}
	}
	if len(contract.Enums) != 1 || contract.Enums[0].GraphQLName != "UserStatus" || len(contract.Enums[0].Values) != 2 {
		t.Fatalf("normalized enum contract = %#v", contract.Enums)
	}
	enumNames := map[string]bool{}
	for _, value := range contract.Enums[0].Values {
		enumNames[value.GraphQLName] = true
	}
	if !enumNames["UserStatusActive"] || !enumNames["UserStatusDisabled"] {
		t.Fatalf("normalized enum value names = %#v", contract.Enums[0].Values)
	}
	if batch := contract.Models[0].Computed[0]; batch.Name != "batchGreeting" || batch.Batch == nil || batch.Batch.CacheKey == nil || batch.Batch.MaxBatchSize != 64 {
		t.Fatalf("batch computed contract = %#v", contract.Models[0].Computed)
	}
	if len(contract.CustomOperations) != 2 || contract.CustomOperations[0].Name != "importUsers" || contract.CustomOperations[1].Name != "searchUsers" || contract.CustomOperations[0].Capability != ir.CustomOperationCallerOnly {
		t.Fatalf("custom contract = %#v", contract.CustomOperations)
	}
	mutation := contract.CustomOperations[0]
	argumentKinds := map[string]ir.GraphQLTypeKind{}
	for _, argument := range mutation.Arguments {
		argumentKinds[argument.Name] = argument.Type.Kind
	}
	if mutation.Operation != ir.CustomOperationMutation || len(mutation.Arguments) != 3 || argumentKinds["data"] != ir.GraphQLTypeCreateInput || argumentKinds["patch"] != ir.GraphQLTypeUpdateManyInput || argumentKinds["metadata"] != ir.GraphQLTypeScalar {
		t.Fatalf("custom mutation typed arguments = %#v", mutation)
	}
	second := Compile(context.Background(), Config{Dir: "testdata/graphql_extensions", Pattern: ".", Root: "DefineSchema"})
	if second.Compilation == nil || second.ContractFingerprint != result.ContractFingerprint || second.ModelFingerprint != result.ModelFingerprint {
		t.Fatalf("extension compilation is not deterministic: first=%#v second=%#v", result, second)
	}
}

func TestGraphQLOnlyChangesAffectContractFingerprintNotModelPhysicalOrMigration(t *testing.T) {
	compiled := Compile(context.Background(), Config{Dir: "testdata/graphql_extensions", Pattern: ".", Root: "DefineSchema"})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	before := *compiled.Compilation
	after := before
	after.Contract.Models = append([]ir.ModelContractIR(nil), before.Contract.Models...)
	after.Contract.Models[0].GraphQLName += "TransportRename"
	after.Contract.Models[0].Roots.FindOne += "TransportRename"
	beforeModel, err := ir.ModelFingerprint(before.Model)
	if err != nil {
		t.Fatal(err)
	}
	afterModel, err := ir.ModelFingerprint(after.Model)
	if err != nil {
		t.Fatal(err)
	}
	beforeContract, err := ir.ContractFingerprint(before.Contract)
	if err != nil {
		t.Fatal(err)
	}
	afterContract, err := ir.ContractFingerprint(after.Contract)
	if err != nil {
		t.Fatal(err)
	}
	if beforeModel != afterModel || beforeContract == afterContract {
		t.Fatalf("fingerprints model=%s/%s contract=%s/%s", beforeModel, afterModel, beforeContract, afterContract)
	}

	providers := []struct {
		name      string
		namespace physical.PhysicalName
		lower     func(context.Context, ir.ModelIR, physical.LowerOptions) (physical.PhysicalSchema, error)
	}{
		{name: "sqlite", namespace: "main", lower: sqlite.New().Lower},
		{name: "postgresql", namespace: "graphql_contract_only", lower: postgresql.New().Lower},
	}
	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			left, err := provider.lower(context.Background(), before.Model, physical.LowerOptions{Namespace: provider.namespace})
			if err != nil {
				t.Fatal(err)
			}
			right, err := provider.lower(context.Background(), after.Model, physical.LowerOptions{Namespace: provider.namespace})
			if err != nil {
				t.Fatal(err)
			}
			leftFingerprint, err := physical.PhysicalFingerprint(left)
			if err != nil {
				t.Fatal(err)
			}
			rightFingerprint, err := physical.PhysicalFingerprint(right)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := migration.Diff(left, right)
			if err != nil {
				t.Fatal(err)
			}
			physicalOperations := 0
			for _, operation := range plan.Operations {
				if operation.Kind != migration.RecordSchemaVersion {
					physicalOperations++
				}
			}
			if leftFingerprint != rightFingerprint || physicalOperations != 0 {
				t.Fatalf("contract-only change physical=%s/%s migration=%#v", leftFingerprint, rightFingerprint, plan.Operations)
			}
		})
	}
}

func TestGraphQLContractRejectsEveryNameExposureTypeAndReservedCollision(t *testing.T) {
	invalid := ir.CompilationIR{Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{
		{ModelID: "a", GraphQLName: "__Post", GraphQLPlural: "posts", Exposed: true, Operations: []ir.Operation{ir.OperationFindOne}, Roots: ir.GraphQLRootNamesIR{FindOne: "node"}, Fields: []ir.FieldContractIR{{FieldID: "a1", GraphQLName: "same"}, {FieldID: "a2", GraphQLName: "same"}}, Limits: ir.LimitContractIR{DefaultPageSize: 10, MaxPageSize: 5}},
		{ModelID: "b", GraphQLName: "Query", GraphQLPlural: "queries", Exposed: true, Operations: []ir.Operation{ir.OperationFindOne, ir.OperationFindOne, "invented"}, Roots: ir.GraphQLRootNamesIR{FindOne: "node"}},
	}, Enums: []ir.EnumContractIR{{EnumID: "enum", GraphQLName: "Query", Values: []ir.EnumValueContractIR{{ValueID: "v1", GraphQLName: "SAME"}, {ValueID: "v2", GraphQLName: "SAME"}}}}}}
	diagnostics := graphqlcontract.Normalize(&invalid, nil)
	for _, code := range []string{"P5_GRAPHQL_MODEL_NAME", "P5_GRAPHQL_FIELD_COLLISION", "P5_GRAPHQL_PAGE_LIMIT", "P5_GRAPHQL_OPERATION_DUPLICATE", "P5_GRAPHQL_OPERATION_UNKNOWN", "P5_GRAPHQL_ROOT_COLLISION", "P5_GRAPHQL_TYPE_COLLISION", "P5_GRAPHQL_ENUM_VALUE_COLLISION"} {
		if !compileHasDiagnostic(diagnostics, code) {
			t.Errorf("missing %s in %#v", code, diagnostics)
		}
	}

	capabilities := Compile(context.Background(), Config{Dir: "testdata/graphql_capabilities", Pattern: ".", Root: "DefineSchema"})
	if capabilities.Compilation != nil || !compileHasDiagnostic(capabilities.Diagnostics, "P5_CUSTOM_SIGNATURE") || !compileHasDiagnostic(capabilities.Diagnostics, "P5_EXTENSION_ARGUMENT_TYPE") {
		t.Fatalf("unknown/capability type diagnostics = %#v", capabilities.Diagnostics)
	}

	social := Compile(context.Background(), Config{Dir: "testdata/social", Pattern: ".", Root: "DefineSchema"})
	if len(social.Diagnostics) != 0 || social.Compilation == nil {
		t.Fatalf("social diagnostics = %#v", social.Diagnostics)
	}
	hidden := *social.Compilation
	hidden.Contract.Models = append([]ir.ModelContractIR(nil), social.Compilation.Contract.Models...)
	for index := range hidden.Contract.Models {
		if hidden.Contract.Models[index].GraphQLName == "User" {
			hidden.Contract.Models[index].Exposed = false
		}
	}
	if _, err := graphqlschema.Build(hidden); err == nil || !strings.Contains(err.Error(), "targets hidden model") {
		t.Fatalf("hidden relation target error = %v", err)
	}

	reserved := *social.Compilation
	post := ir.ModelContractIR{}
	for _, model := range reserved.Contract.Models {
		if model.GraphQLName == "Post" {
			post = model
		}
	}
	custom := graphqlextension.CustomOperationDeclaration{Operation: ir.CustomOperationContractIR{
		ExtensionID: "reserved-collision", Operation: ir.CustomOperationQuery, Name: post.Roots.Aggregate,
		Result:   ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Boolean"},
		Resolver: ir.AttachedMethodIR{PackagePath: "example.test/social", Name: "AggregatePosts"}, Capability: ir.CustomOperationCallerOnly,
	}}
	reservedDiagnostics := graphqlextension.Normalize(&reserved, nil, []graphqlextension.CustomOperationDeclaration{custom})
	if !compileHasDiagnostic(reservedDiagnostics, "P5_CUSTOM_ROOT_COLLISION") {
		t.Fatalf("reserved-root diagnostics = %#v", reservedDiagnostics)
	}
}

func TestCompileRejectsCustomRootCollisionFromSource(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/graphql_collision", Pattern: ".", Root: "DefineSchema"})
	if result.Compilation != nil || !compileHasDiagnostic(result.Diagnostics, "P5_CUSTOM_ROOT_COLLISION") {
		t.Fatalf("collision diagnostics = %#v", result.Diagnostics)
	}
}

func TestCompileRejectsCustomSystemTxDBAndRawSQLShapedCapabilitiesFromSource(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/graphql_capabilities", Pattern: ".", Root: "DefineSchema"})
	if result.Compilation != nil || !compileHasDiagnostic(result.Diagnostics, "P5_CUSTOM_SIGNATURE") || !compileHasDiagnostic(result.Diagnostics, "P5_EXTENSION_ARGUMENT_TYPE") {
		t.Fatalf("capability diagnostics = %#v", result.Diagnostics)
	}
}

func compileHasDiagnostic(diagnostics []ir.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestCompileRawDeterministicUnderShuffledDeclarations(t *testing.T) {
	extracted := schema.Extract(context.Background(), schema.Config{Dir: "testdata/social", Pattern: ".", Root: "DefineSchema"})
	if len(extracted.Diagnostics) != 0 {
		t.Fatalf("extract diagnostics: %#v", extracted.Diagnostics)
	}
	// CompileRaw is source-independent and intentionally does not interpret
	// GolemModel; supply the Tag relation identity as a common declaration.
	for modelIndex := range extracted.Raw.Models {
		if extracted.Raw.Models[modelIndex].GoName != "Tag" {
			continue
		}
		for fieldIndex := range extracted.Raw.Models[modelIndex].Fields {
			if extracted.Raw.Models[modelIndex].Fields[fieldIndex].GoName == "Name" {
				extracted.Raw.Models[modelIndex].Fields[fieldIndex].GolemAttrs = append(extracted.Raw.Models[modelIndex].Fields[fieldIndex].GolemAttrs, ir.RawAttribute{Name: "unique"})
			}
		}
	}
	var baseline []byte
	for seed := int64(0); seed < 30; seed++ {
		raw := extracted.Raw
		raw.Models = append([]ir.RawModelDecl(nil), raw.Models...)
		raw.Root.Models = append([]ir.RawModelRef(nil), raw.Root.Models...)
		random := rand.New(rand.NewSource(seed))
		random.Shuffle(len(raw.Models), func(i, j int) { raw.Models[i], raw.Models[j] = raw.Models[j], raw.Models[i] })
		random.Shuffle(len(raw.Root.Models), func(i, j int) { raw.Root.Models[i], raw.Root.Models[j] = raw.Root.Models[j], raw.Root.Models[i] })
		result := CompileRaw(raw)
		if len(result.Diagnostics) != 0 {
			t.Fatalf("seed %d diagnostics: %#v", seed, result.Diagnostics)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if seed == 0 {
			baseline = encoded
		} else if string(encoded) != string(baseline) {
			t.Fatalf("seed %d changed canonical output", seed)
		}
	}
}

func TestAdvancedSourceCompileIsByteStable(t *testing.T) {
	left := Compile(context.Background(), Config{Dir: "testdata/social", Pattern: "."})
	right := Compile(context.Background(), Config{Dir: "testdata/social", Pattern: "."})
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	if string(leftJSON) != string(rightJSON) {
		t.Fatal("advanced compile/inspect payload is not byte stable")
	}
}

func TestProviderScopedRelationOptionsAreRejected(t *testing.T) {
	model := ir.ModelIR{Relations: []ir.RelationIR{{ID: "relation", ForeignKey: &ir.ForeignKeyIR{ID: "fk"}}}}
	action := ir.ActionCascade
	diagnostics := applyRelationOptions(&model, []methods.RelationOptionDeclaration{{RelationID: "relation", OnDelete: &action, Provider: ir.ProviderScopePostgreSQL}})
	if !containsCode(diagnostics, "P1_RELATION_OPTION_PROVIDER_UNSUPPORTED") || model.Relations[0].ForeignKey.OnDelete == action {
		t.Fatalf("provider-specific relation option was not atomically rejected: %#v", diagnostics)
	}
}

func TestRelationActionRequirementsAndAtomicity(t *testing.T) {
	model := ir.ModelIR{
		Models:    []ir.ModelDeclIR{{ID: "source", Fields: []ir.FieldIR{{ID: "local", Scalar: &ir.ScalarFieldIR{Nullable: false}}}}},
		Relations: []ir.RelationIR{{ID: "relation", SourceModel: "source", SourceField: "relation-field", LocalFields: []ir.FieldID{"local"}, ForeignKey: &ir.ForeignKeyIR{ID: "fk", OnDelete: ir.ActionNoAction}}},
	}
	setNull, cascade := ir.ActionSetNull, ir.ActionCascade
	options := []methods.RelationOptionDeclaration{
		{ModelID: "source", RelationID: "relation", RelationField: "relation-field", OnDelete: &cascade, Provider: ir.ProviderScopePortable},
		{ModelID: "source", RelationID: "relation", RelationField: "relation-field", OnUpdate: &setNull, Provider: ir.ProviderScopePortable},
	}
	diagnostics := applyRelationOptions(&model, options)
	if !containsCode(diagnostics, "P1_RELATION_OPTION_DUPLICATE") || !containsCode(diagnostics, "P1_RELATION_SET_NULL") || model.Relations[0].ForeignKey.OnDelete != ir.ActionNoAction {
		t.Fatalf("invalid batch was not rejected atomically: %#v", diagnostics)
	}
}

func TestInvalidCompilationIsAtomic(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/invalid", Pattern: ".", Root: "DefineSchema"})
	if result.Compilation != nil || result.ModelFingerprint != "" || result.ContractFingerprint != "" {
		t.Fatalf("invalid input exposed partial accepted output: %#v", result)
	}
	if !containsCode(result.Diagnostics, "P1_PRIMARY_KEY_MISSING") {
		t.Fatalf("missing complete-validation diagnostic: %#v", result.Diagnostics)
	}
}

func TestReviewedModelAndFieldRenameTransfersIdentityAndPhysicalRename(t *testing.T) {
	baseRaw := renameFixtureRaw()
	initial := CompileRaw(baseRaw)
	if len(initial.Diagnostics) != 0 || initial.Compilation == nil {
		t.Fatalf("initial diagnostics=%#v", initial.Diagnostics)
	}
	previous := initial.Compilation.Model
	oldModel := previous.Models[0]
	oldField := fieldByGoName(t, oldModel, "Name")
	if oldModel.CanonicalIdentity == "" || oldField.CanonicalIdentity == "" {
		t.Fatal("initial ModelIR did not preserve canonical identities")
	}

	renamedRaw := renameFixtureRaw()
	renamedRaw.Root.Models[0].GoName = "Member"
	renamedRaw.Models[0].GoName = "Member"
	setRawAttribute(&renamedRaw.Models[0].Marker, "table", "members")
	addRawAttribute(&renamedRaw.Models[0].Marker, "renameFrom", oldModel.CanonicalIdentity)
	for index := range renamedRaw.Models[0].Fields {
		field := &renamedRaw.Models[0].Fields[index]
		if field.GoName == "Name" {
			field.GoName = "DisplayName"
			column := "display_name"
			field.DBTag = &column
			addRawAttribute(&field.GolemAttrs, "renameFrom", oldField.CanonicalIdentity)
		}
	}
	renamed := CompileRawWithPrevious(renamedRaw, &previous)
	if len(renamed.Diagnostics) != 0 || renamed.Compilation == nil {
		t.Fatalf("rename diagnostics=%#v", renamed.Diagnostics)
	}
	currentModel := renamed.Compilation.Model.Models[0]
	currentField := fieldByGoName(t, currentModel, "DisplayName")
	if currentModel.ID != oldModel.ID || currentModel.CanonicalIdentity != oldModel.CanonicalIdentity || currentField.ID != oldField.ID || currentField.CanonicalIdentity != oldField.CanonicalIdentity {
		t.Fatalf("identity was not transferred: old=%#v/%#v new=%#v/%#v", oldModel, oldField, currentModel, currentField)
	}

	provider := postgresql.New()
	beforePhysical, err := provider.Lower(context.Background(), previous, physical.LowerOptions{Namespace: "rename_test"})
	if err != nil {
		t.Fatal(err)
	}
	afterPhysical, err := provider.Lower(context.Background(), renamed.Compilation.Model, physical.LowerOptions{Namespace: "rename_test"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migration.Diff(beforePhysical, afterPhysical)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[migration.OperationKind]int{}
	for _, operation := range plan.Operations {
		kinds[operation.Kind]++
	}
	if kinds[migration.RenameTable] != 1 || kinds[migration.RenameColumn] != 1 || kinds[migration.CreateTable] != 0 || kinds[migration.DropTable] != 0 || kinds[migration.AddColumn] != 0 || kinds[migration.DropColumn] != 0 {
		t.Fatalf("physical rename plan=%#v", plan.Operations)
	}

	withoutRename := renamedRaw
	withoutRename.Models = append([]ir.RawModelDecl(nil), renamedRaw.Models...)
	withoutRename.Models[0].Marker = removeRawAttribute(renamedRaw.Models[0].Marker, "renameFrom")
	withoutRename.Models[0].Fields = append([]ir.RawFieldDecl(nil), renamedRaw.Models[0].Fields...)
	for index := range withoutRename.Models[0].Fields {
		withoutRename.Models[0].Fields[index].GolemAttrs = removeRawAttribute(withoutRename.Models[0].Fields[index].GolemAttrs, "renameFrom")
	}
	renamedHead := renamed.Compilation.Model
	persisted := CompileRawWithPrevious(withoutRename, &renamedHead)
	if len(persisted.Diagnostics) != 0 || persisted.Compilation == nil || persisted.Compilation.Model.Models[0].ID != oldModel.ID || fieldByGoName(t, persisted.Compilation.Model.Models[0], "DisplayName").ID != oldField.ID {
		t.Fatalf("transferred identity did not persist after renameFrom removal: %#v", persisted.Diagnostics)
	}
}

func TestCompileExtractsDefaultIdentityRenameByReviewedStableID(t *testing.T) {
	fixture := t.TempDir()
	moduleDir, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	writeRenameSourceFixture(t, fixture, moduleDir, "User", "users", "Name", "name", "", "")
	initial := Compile(context.Background(), Config{Dir: fixture, Pattern: ".", Root: "DefineSchema"})
	if len(initial.Diagnostics) != 0 || initial.Compilation == nil {
		t.Fatalf("initial source diagnostics=%#v", initial.Diagnostics)
	}
	previous := initial.Compilation.Model
	oldModel := previous.Models[0]
	oldField := fieldByGoName(t, oldModel, "Name")
	if strings.ContainsRune(oldModel.CanonicalIdentity, '\x00') == false || strings.ContainsRune(oldField.CanonicalIdentity, '\x00') == false {
		t.Fatalf("fixture no longer exercises non-source-authorable default canonical identities: model=%q field=%q", oldModel.CanonicalIdentity, oldField.CanonicalIdentity)
	}

	writeRenameSourceFixture(t, fixture, moduleDir, "Member", "members", "DisplayName", "display_name", string(oldModel.ID), string(oldField.ID))
	withoutHistory := Compile(context.Background(), Config{Dir: fixture, Pattern: ".", Root: "DefineSchema"})
	if !containsCode(withoutHistory.Diagnostics, "P1_RENAME_HISTORY_REQUIRED") {
		t.Fatalf("ordinary Compile accepted source-authored rename without history: %#v", withoutHistory.Diagnostics)
	}
	renamed := Compile(context.Background(), Config{Dir: fixture, Pattern: ".", Root: "DefineSchema", PreviousModel: &previous})
	if len(renamed.Diagnostics) != 0 || renamed.Compilation == nil {
		t.Fatalf("source rename diagnostics=%#v", renamed.Diagnostics)
	}
	currentModel := renamed.Compilation.Model.Models[0]
	currentField := fieldByGoName(t, currentModel, "DisplayName")
	if currentModel.ID != oldModel.ID || currentField.ID != oldField.ID || currentModel.CanonicalIdentity != oldModel.CanonicalIdentity || currentField.CanonicalIdentity != oldField.CanonicalIdentity {
		t.Fatalf("stable-ID aliases did not transfer default identities: model=%#v field=%#v", currentModel, currentField)
	}

	writeRenameSourceFixture(t, fixture, moduleDir, "Member", "members", "DisplayName", "display_name", "", "")
	renamedHead := renamed.Compilation.Model
	persisted := Compile(context.Background(), Config{Dir: fixture, Pattern: ".", Root: "DefineSchema", PreviousModel: &renamedHead})
	if len(persisted.Diagnostics) != 0 || persisted.Compilation == nil || persisted.Compilation.Model.Models[0].ID != oldModel.ID || fieldByGoName(t, persisted.Compilation.Model.Models[0], "DisplayName").ID != oldField.ID {
		t.Fatalf("source identity did not persist after one-time annotations were removed: %#v", persisted.Diagnostics)
	}
}

func TestReviewedRelationFieldRenameTransfersIdentity(t *testing.T) {
	raw := relationRenameFixtureRaw()
	initial := CompileRaw(raw)
	if len(initial.Diagnostics) != 0 || initial.Compilation == nil {
		t.Fatalf("initial relation diagnostics=%#v", initial.Diagnostics)
	}
	previous := initial.Compilation.Model
	var oldField ir.FieldIR
	for _, model := range previous.Models {
		if model.Go.Name == "User" {
			oldField = fieldByGoName(t, model, "Posts")
		}
	}
	if oldField.Kind != ir.FieldRelation {
		t.Fatalf("expected relation field, got %#v", oldField)
	}
	for modelIndex := range raw.Models {
		if raw.Models[modelIndex].GoName != "User" {
			continue
		}
		for fieldIndex := range raw.Models[modelIndex].Fields {
			field := &raw.Models[modelIndex].Fields[fieldIndex]
			if field.GoName == "Posts" {
				field.GoName = "Articles"
				addRawAttribute(&field.GolemAttrs, "renameFrom", string(oldField.ID))
			}
		}
	}
	renamed := CompileRawWithPrevious(raw, &previous)
	if len(renamed.Diagnostics) != 0 || renamed.Compilation == nil {
		t.Fatalf("relation rename diagnostics=%#v", renamed.Diagnostics)
	}
	var currentField ir.FieldIR
	for _, model := range renamed.Compilation.Model.Models {
		if model.Go.Name == "User" {
			currentField = fieldByGoName(t, model, "Articles")
		}
	}
	if currentField.Kind != ir.FieldRelation || currentField.ID != oldField.ID || currentField.CanonicalIdentity != oldField.CanonicalIdentity {
		t.Fatalf("relation identity was not transferred: old=%#v current=%#v", oldField, currentField)
	}
	linked := false
	for _, relation := range renamed.Compilation.Model.Relations {
		if relation.InverseField != nil && *relation.InverseField == currentField.ID {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("transferred relation field %s is not referenced by a linked relation", currentField.ID)
	}
}

func relationRenameFixtureRaw() ir.RawDeclIR {
	userID, postID, authorID, ignored := "id", "id", "author_id", "-"
	named := func(name string) ir.RawGoTypeRef {
		return ir.RawGoTypeRef{Kind: ir.RawGoTypeNamed, PackagePath: "example/models", GoName: name, Args: []ir.RawGoTypeRef{}}
	}
	relationType := func(kind ir.RawGoTypeKind, name string) ir.RawGoTypeRef {
		return ir.RawGoTypeRef{Kind: kind, Args: []ir.RawGoTypeRef{named(name)}}
	}
	relationAttrs := func(kind, fields, references string) []ir.RawAttribute {
		return []ir.RawAttribute{rawValue("relation", kind), rawValue("fields", fields), rawValue("references", references)}
	}
	return ir.RawDeclIR{
		FormatVersion: ir.RawDeclFormatVersion,
		Root: ir.RawSchemaDecl{
			PackagePath: "example/schema", FunctionName: "DefineSchema", SchemaName: "relation_rename",
			Actor:     &ir.RawNamedTypeRef{PackagePath: "example/schema", GoName: "Actor"},
			Providers: []ir.RawProviderRef{{Provider: ir.SQLite}, {Provider: ir.PostgreSQL}},
			Models:    []ir.RawModelRef{{PackagePath: "example/models", GoName: "User"}, {PackagePath: "example/models", GoName: "Post"}},
		},
		Models: []ir.RawModelDecl{
			{PackagePath: "example/models", GoName: "User", Marker: []ir.RawAttribute{{Name: "model"}, rawValue("table", "users")}, Fields: []ir.RawFieldDecl{
				{GoName: "ID", DBTag: &userID, GoType: ir.RawGoTypeRef{Kind: ir.RawGoTypeBuiltin, GoName: "int64"}, GolemAttrs: []ir.RawAttribute{{Name: "pk"}}},
				{GoName: "Posts", DBTag: &ignored, GoType: relationType(ir.RawGoTypeSlice, "Post"), GolemAttrs: relationAttrs("has_many", "id", "author_id")},
			}},
			{PackagePath: "example/models", GoName: "Post", Marker: []ir.RawAttribute{{Name: "model"}, rawValue("table", "posts")}, Fields: []ir.RawFieldDecl{
				{GoName: "ID", DBTag: &postID, GoType: ir.RawGoTypeRef{Kind: ir.RawGoTypeBuiltin, GoName: "int64"}, GolemAttrs: []ir.RawAttribute{{Name: "pk"}}},
				{GoName: "AuthorID", DBTag: &authorID, GoType: ir.RawGoTypeRef{Kind: ir.RawGoTypeBuiltin, GoName: "int64"}},
				{GoName: "Author", DBTag: &ignored, GoType: relationType(ir.RawGoTypePointer, "User"), GolemAttrs: relationAttrs("belongs_to", "author_id", "id")},
			}},
		},
	}
}

func writeRenameSourceFixture(t *testing.T, directory, golemModule, modelName, table, fieldName, column, modelRename, fieldRename string) {
	t.Helper()
	goMod := fmt.Sprintf("module example.com/renamefixture\n\ngo 1.23.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => %s\n", filepath.ToSlash(golemModule))
	modelRenameTag := ""
	if modelRename != "" {
		modelRenameTag = ";renameFrom=" + modelRename
	}
	fieldRenameTag := ""
	if fieldRename != "" {
		fieldRenameTag = ` golem:"renameFrom=` + fieldRename + `"`
	}
	source := fmt.Sprintf(`package renamefixture

import golem "github.com/eleven-am/golem/go/golem"

type Actor struct { UserID int64 }

type %[1]s struct {
	_ struct{} `+"`"+`golem:"model;table=%[2]s%[3]s"`+"`"+`
	ID int64 `+"`"+`db:"id" golem:"pk"`+"`"+`
	%[4]s string `+"`"+`db:"%[5]s"%[6]s`+"`"+`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "rename_source")
	golem.Actor[Actor](schema)
	golem.Model[%[1]s](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
`, modelName, table, modelRenameTag, fieldName, column, fieldRenameTag)
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "schema.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRenameFromRequiresExactReviewedTarget(t *testing.T) {
	base := CompileRaw(renameFixtureRaw())
	if len(base.Diagnostics) != 0 {
		t.Fatal(base.Diagnostics)
	}
	previous := base.Compilation.Model
	modelIdentity := previous.Models[0].CanonicalIdentity
	modelStableID := string(previous.Models[0].ID)
	previousField := fieldByGoName(t, previous.Models[0], "Name")
	fieldIdentity := previousField.CanonicalIdentity
	fieldStableID := string(previousField.ID)

	tests := []struct {
		name string
		edit func(*ir.RawDeclIR)
		code string
	}{
		{"missing history", func(raw *ir.RawDeclIR) { addRawAttribute(&raw.Models[0].Marker, "renameFrom", modelIdentity) }, "P1_RENAME_HISTORY_REQUIRED"},
		{"missing target", func(raw *ir.RawDeclIR) { addRawAttribute(&raw.Models[0].Marker, "renameFrom", "missing.Model") }, "P1_RENAME_TARGET_MISSING"},
		{"wrong model kind", func(raw *ir.RawDeclIR) { addRawAttribute(&raw.Models[0].Marker, "renameFrom", fieldStableID) }, "P1_RENAME_WRONG_KIND"},
		{"wrong field kind", func(raw *ir.RawDeclIR) {
			addRawAttribute(&raw.Models[0].Fields[1].GolemAttrs, "renameFrom", modelStableID)
		}, "P1_RENAME_WRONG_KIND"},
		{"target still present", func(raw *ir.RawDeclIR) {
			raw.Models[0].Fields[1].GoName = "DisplayName"
			addRawAttribute(&raw.Models[0].Fields[1].GolemAttrs, "renameFrom", fieldIdentity)
			old := raw.Models[0].Fields[1]
			old.GoName = "Name"
			old.GolemAttrs = removeRawAttribute(old.GolemAttrs, "renameFrom")
			raw.Models[0].Fields = append(raw.Models[0].Fields, old)
		}, "P1_RENAME_TARGET_PRESENT"},
		{"duplicate claim through canonical and stable-ID aliases", func(raw *ir.RawDeclIR) {
			addRawAttribute(&raw.Models[0].Fields[0].GolemAttrs, "renameFrom", fieldIdentity)
			addRawAttribute(&raw.Models[0].Fields[1].GolemAttrs, "renameFrom", fieldStableID)
		}, "P1_RENAME_DUPLICATE_CLAIM"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := renameFixtureRaw()
			test.edit(&raw)
			var result Result
			if test.name == "missing history" {
				result = CompileRaw(raw)
			} else {
				result = CompileRawWithPrevious(raw, &previous)
			}
			if !containsCode(result.Diagnostics, test.code) {
				t.Fatalf("diagnostics=%#v want=%s", result.Diagnostics, test.code)
			}
		})
	}
	ambiguous := previous
	duplicate := previous.Models[0]
	duplicate.Go.Name = "DuplicateHistory"
	ambiguous.Models = append(append([]ir.ModelDeclIR(nil), previous.Models...), duplicate)
	if result := CompileRawWithPrevious(renameFixtureRaw(), &ambiguous); !containsCode(result.Diagnostics, "P1_PREVIOUS_IDENTITY_AMBIGUOUS") {
		t.Fatalf("ambiguous previous history diagnostics=%#v", result.Diagnostics)
	}
	invalidVersion := previous
	invalidVersion.FormatVersion++
	if result := CompileRawWithPrevious(renameFixtureRaw(), &invalidVersion); !containsCode(result.Diagnostics, "P1_PREVIOUS_MODEL_INVALID") {
		t.Fatalf("invalid previous format diagnostics=%#v", result.Diagnostics)
	}
}

func TestRenameFromRejectsCrossModelScopeAndIsDeterministic(t *testing.T) {
	raw := renameFixtureRaw()
	second := raw.Models[0]
	second.Marker = append([]ir.RawAttribute(nil), raw.Models[0].Marker...)
	second.GoName = "Team"
	setRawAttribute(&second.Marker, "table", "teams")
	second.Fields = append([]ir.RawFieldDecl(nil), second.Fields...)
	for index := range second.Fields {
		second.Fields[index].GolemAttrs = append([]ir.RawAttribute(nil), second.Fields[index].GolemAttrs...)
		second.Fields[index].GoName = "Team" + second.Fields[index].GoName
		column := strings.ToLower(second.Fields[index].GoName)
		second.Fields[index].DBTag = &column
	}
	raw.Models = append(raw.Models, second)
	raw.Root.Models = append(raw.Root.Models, ir.RawModelRef{PackagePath: second.PackagePath, GoName: second.GoName, Ordinal: 1})
	base := CompileRaw(raw)
	if len(base.Diagnostics) != 0 {
		t.Fatalf("base diagnostics=%#v", base.Diagnostics)
	}
	previous := base.Compilation.Model
	var user ir.ModelDeclIR
	for _, model := range previous.Models {
		if model.Go.Name == "User" {
			user = model
		}
	}
	target := fieldByGoName(t, user, "Name").CanonicalIdentity
	for modelIndex := range raw.Models {
		if raw.Models[modelIndex].GoName == "Team" {
			addRawAttribute(&raw.Models[modelIndex].Fields[1].GolemAttrs, "renameFrom", target)
		}
	}
	result := CompileRawWithPrevious(raw, &previous)
	if !containsCode(result.Diagnostics, "P1_RENAME_SCOPE") {
		t.Fatalf("cross-model diagnostics=%#v", result.Diagnostics)
	}

	for seed := int64(0); seed < 10; seed++ {
		candidate := raw
		candidate.Models = append([]ir.RawModelDecl(nil), raw.Models...)
		rand.New(rand.NewSource(seed)).Shuffle(len(candidate.Models), func(i, j int) { candidate.Models[i], candidate.Models[j] = candidate.Models[j], candidate.Models[i] })
		got := CompileRawWithPrevious(candidate, &previous)
		if !containsCode(got.Diagnostics, "P1_RENAME_SCOPE") {
			t.Fatalf("seed %d diagnostics=%#v", seed, got.Diagnostics)
		}
	}
}

func renameFixtureRaw() ir.RawDeclIR {
	dbID, dbName := "id", "name"
	return ir.RawDeclIR{FormatVersion: ir.RawDeclFormatVersion, Root: ir.RawSchemaDecl{PackagePath: "example/schema", FunctionName: "DefineSchema", SchemaName: "rename", Actor: &ir.RawNamedTypeRef{PackagePath: "example/schema", GoName: "Actor"}, Providers: []ir.RawProviderRef{{Provider: ir.SQLite}, {Provider: ir.PostgreSQL}}, Models: []ir.RawModelRef{{PackagePath: "example/models", GoName: "User"}}}, Models: []ir.RawModelDecl{{PackagePath: "example/models", GoName: "User", Marker: []ir.RawAttribute{{Name: "model"}, rawValue("table", "users")}, Fields: []ir.RawFieldDecl{
		{GoName: "ID", DBTag: &dbID, GoType: ir.RawGoTypeRef{Kind: ir.RawGoTypeBuiltin, GoName: "int64"}, GolemAttrs: []ir.RawAttribute{{Name: "pk"}}},
		{GoName: "Name", DBTag: &dbName, GoType: ir.RawGoTypeRef{Kind: ir.RawGoTypeBuiltin, GoName: "string"}},
	}}}}
}

func rawValue(name, value string) ir.RawAttribute {
	return ir.RawAttribute{Name: name, RawValue: &value}
}

func addRawAttribute(attributes *[]ir.RawAttribute, name, value string) {
	*attributes = append(*attributes, rawValue(name, value))
}

func setRawAttribute(attributes *[]ir.RawAttribute, name, value string) {
	for index := range *attributes {
		if (*attributes)[index].Name == name {
			(*attributes)[index].RawValue = &value
			return
		}
	}
	addRawAttribute(attributes, name, value)
}

func removeRawAttribute(attributes []ir.RawAttribute, name string) []ir.RawAttribute {
	result := make([]ir.RawAttribute, 0, len(attributes))
	for _, attribute := range attributes {
		if attribute.Name != name {
			result = append(result, attribute)
		}
	}
	return result
}

func fieldByGoName(t *testing.T, model ir.ModelDeclIR, name string) ir.FieldIR {
	t.Helper()
	for _, field := range model.Fields {
		if field.GoName == name {
			return field
		}
	}
	t.Fatalf("field %s missing from %#v", name, model)
	return ir.FieldIR{}
}

func assertCompositePrimary(t *testing.T, model ir.ModelIR, logicalName string) {
	t.Helper()
	for _, entry := range model.Models {
		if entry.LogicalName == logicalName {
			if entry.PrimaryKey == nil || len(entry.PrimaryKey.Fields) != 2 {
				t.Fatalf("%s primary key: %#v", logicalName, entry.PrimaryKey)
			}
			return
		}
	}
	t.Fatalf("missing model %s", logicalName)
}

func containsCode(diagnostics []ir.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
