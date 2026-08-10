package contract

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestLowerCamelFrozenInitialisms(t *testing.T) {
	want := map[string]string{"ID": "id", "URLValue": "urlValue", "CreatedAt": "createdAt", "X": "x", "already": "already"}
	for input, expected := range want {
		if actual := LowerCamel(input); actual != expected {
			t.Errorf("LowerCamel(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestNormalizeMaterializesAndOverridesGraphQLContract(t *testing.T) {
	modelID := ir.ModelID("10000000000000000000000000000000")
	compilation := ir.CompilationIR{Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{{
		ModelID: modelID, GraphQLName: "Post", Exposed: true,
		Fields: []ir.FieldContractIR{{FieldID: "20000000000000000000000000000000", GraphQLName: "id", Modes: []ir.FieldMode{ir.ModeVisible}}},
	}}}}
	operations := []ir.Operation{ir.OperationFindOne, ir.OperationFindMany}
	plural := "articles"
	roots := ir.GraphQLRootNamesIR{FindOne: "article"}
	defaultPage, maxPage := uint32(25), uint32(250)
	diagnostics := Normalize(&compilation, []ModelPatch{{ModelID: modelID, Operations: &operations, Plural: &plural, Roots: &roots, DefaultPage: &defaultPage, MaximumPage: &maxPage}})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	model := compilation.Contract.Models[0]
	if compilation.Contract.GraphQLABIVersion != ABIVersion || model.GraphQLPlural != "articles" || model.Roots.FindOne != "article" || model.Roots.FindMany != "articles" || model.Limits.DefaultPageSize != 25 || model.Limits.MaxPageSize != 250 {
		t.Fatalf("normalized model = %#v", model)
	}
	if model.Subscriptions || model.Event != nil {
		t.Fatal("subscriptions did not default off")
	}
	if !reflect.DeepEqual(model.Operations, operations) {
		t.Fatalf("operations = %#v", model.Operations)
	}
}

func TestNormalizeRejectsNamesOperationsLimitsAndRootCollisions(t *testing.T) {
	compilation := ir.CompilationIR{Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{
		{ModelID: "a", GraphQLName: "Post", GraphQLPlural: "posts", Exposed: true, Operations: []ir.Operation{ir.OperationFindOne}, Roots: ir.GraphQLRootNamesIR{FindOne: "node"}, Fields: []ir.FieldContractIR{{FieldID: "a1", GraphQLName: "same"}, {FieldID: "a2", GraphQLName: "same"}}, Limits: ir.LimitContractIR{DefaultPageSize: 10, MaxPageSize: 5}},
		{ModelID: "b", GraphQLName: "User", GraphQLPlural: "users", Exposed: true, Operations: []ir.Operation{ir.OperationFindOne, ir.OperationFindOne, "invented"}, Roots: ir.GraphQLRootNamesIR{FindOne: "node"}},
	}}}
	diagnostics := Normalize(&compilation, nil)
	want := map[string]bool{"P5_GRAPHQL_FIELD_COLLISION": false, "P5_GRAPHQL_PAGE_LIMIT": false, "P5_GRAPHQL_ROOT_COLLISION": false, "P5_GRAPHQL_OPERATION_DUPLICATE": false, "P5_GRAPHQL_OPERATION_UNKNOWN": false}
	for _, diagnostic := range diagnostics {
		if _, ok := want[diagnostic.Code]; ok {
			want[diagnostic.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Errorf("missing %s in %#v", code, diagnostics)
		}
	}
}

func TestGraphQLOnlyChangeDoesNotChangeModelFingerprint(t *testing.T) {
	model := ir.ModelIR{FormatVersion: ir.ModelFormatVersion}
	left := ir.ContractIR{FormatVersion: ir.ContractFormatVersion, GraphQLABIVersion: ABIVersion, Models: []ir.ModelContractIR{{ModelID: "m", GraphQLName: "Post", GraphQLPlural: "posts"}}}
	right := left
	right.Models = append([]ir.ModelContractIR(nil), left.Models...)
	right.Models[0].GraphQLName = "Article"
	leftModel, _ := ir.ModelFingerprint(model)
	rightModel, _ := ir.ModelFingerprint(model)
	leftContract, _ := ir.ContractFingerprint(left)
	rightContract, _ := ir.ContractFingerprint(right)
	if leftModel != rightModel {
		t.Fatal("GraphQL-only change altered ModelFingerprint")
	}
	if leftContract == rightContract {
		t.Fatal("GraphQL-only change did not alter ContractFingerprint")
	}
}

func TestGraphQLHookOwnedRequiresCompleteWritableRequiredRelationAndBeforeCreate(t *testing.T) {
	postID := ir.ModelID("10000000000000000000000000000010")
	userID := ir.ModelID("10000000000000000000000000000020")
	postKey := ir.FieldID("10000000000000000000000000000011")
	authorID := ir.FieldID("10000000000000000000000000000012")
	authorTenant := ir.FieldID("10000000000000000000000000000013")
	authorRelation := ir.FieldID("10000000000000000000000000000014")
	relationID := ir.RelationID("10000000000000000000000000000015")
	slug := ir.FieldID("10000000000000000000000000000016")
	compilation := ir.CompilationIR{
		Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{
			{ID: postID, Fields: []ir.FieldIR{
				{ID: postKey, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}},
				{ID: authorID, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}},
				{ID: authorTenant, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}},
				{ID: authorRelation, Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{RelationID: relationID, Role: ir.RelationSource, Kind: ir.RelationBelongsTo}},
				{ID: slug, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeString}}},
			}, PrimaryKey: &ir.KeyIR{ID: "post-key", Kind: ir.KeyPrimary, Fields: []ir.FieldID{postKey}}},
			{ID: userID},
		}, Relations: []ir.RelationIR{{ID: relationID, SourceModel: postID, TargetModel: userID, SourceField: authorRelation, LocalFields: []ir.FieldID{authorID, authorTenant}, RemoteFields: []ir.FieldID{"user-id", "user-tenant"}}}},
		Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{
			{ModelID: postID, GraphQLName: "Post", Exposed: true, Fields: []ir.FieldContractIR{
				{FieldID: postKey, GraphQLName: "id"}, {FieldID: authorID, GraphQLName: "authorID"},
				{FieldID: authorTenant, GraphQLName: "authorTenant"}, {FieldID: authorRelation, GraphQLName: "author"},
				{FieldID: slug, GraphQLName: "slug"},
			}},
			{ModelID: userID, GraphQLName: "User", Exposed: true},
		}},
	}
	patch := ModelPatch{ModelID: postID, HookOwnedCreateFields: []ir.FieldID{authorID}}
	diagnostics := Normalize(&compilation, []ModelPatch{patch})
	if !hasDiagnostic(diagnostics, "P8_GRAPHQL_HOOK_OWNED_PARTIAL_COMPOSITE") {
		t.Fatalf("partial composite diagnostics=%#v", diagnostics)
	}
	patch.HookOwnedCreateFields = []ir.FieldID{authorID, authorTenant, slug}
	compilation.Contract.Models[0].HookOwnedCreateFields = nil
	diagnostics = Normalize(&compilation, []ModelPatch{patch})
	if len(diagnostics) != 0 {
		t.Fatalf("complete ownership diagnostics=%#v", diagnostics)
	}
	if diagnostics := ValidateHookOwnedMethods(compilation); !hasDiagnostic(diagnostics, "P8_GRAPHQL_HOOK_OWNED_BEFORE_CREATE") {
		t.Fatalf("missing hook diagnostics=%#v", diagnostics)
	}
	modelCopy := postID
	compilation.Contract.Methods = []ir.AttachedMethodIR{{ModelID: &modelCopy, Name: "BeforeCreate", Kind: "hook"}}
	if diagnostics := ValidateHookOwnedMethods(compilation); len(diagnostics) != 0 {
		t.Fatalf("recognized hook diagnostics=%#v", diagnostics)
	}

	invalid := compilation
	invalid.Model.Models = append([]ir.ModelDeclIR(nil), compilation.Model.Models...)
	invalid.Model.Models[0].Fields = append([]ir.FieldIR(nil), compilation.Model.Models[0].Fields...)
	nullableAuthorID := *invalid.Model.Models[0].Fields[1].Scalar
	nullableAuthorID.Nullable = true
	invalid.Model.Models[0].Fields[1].Scalar = &nullableAuthorID
	if diagnostics := validateHookOwnedShape(invalid.Model, invalid.Contract.Models[0], ir.SourceSpan{}); !hasDiagnostic(diagnostics, "P8_GRAPHQL_HOOK_OWNED_REQUIRED") {
		t.Fatalf("nullable ownership diagnostics=%#v", diagnostics)
	}
	invalid = compilation
	invalid.Model.Models = append([]ir.ModelDeclIR(nil), compilation.Model.Models...)
	key := *invalid.Model.Models[0].PrimaryKey
	key.Fields = append(key.Fields, authorID)
	invalid.Model.Models[0].PrimaryKey = &key
	if diagnostics := validateHookOwnedShape(invalid.Model, invalid.Contract.Models[0], ir.SourceSpan{}); !hasDiagnostic(diagnostics, "P8_GRAPHQL_HOOK_OWNED_IDENTITY") {
		t.Fatalf("identity ownership diagnostics=%#v", diagnostics)
	}
}

func hasDiagnostic(values []ir.Diagnostic, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

func TestP7ContractNormalizesSubscriptionsRootsIdentitiesSnapshotsAndLimits(t *testing.T) {
	id := ir.FieldID("10000000000000000000000000000001")
	title := ir.FieldID("10000000000000000000000000000002")
	relation := ir.FieldID("10000000000000000000000000000003")
	modelID := ir.ModelID("10000000000000000000000000000010")
	keyID := ir.KeyID("10000000000000000000000000000011")
	compilation := ir.CompilationIR{
		Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{{
			ID: modelID, Go: ir.GoNamedTypeIR{Name: "Post"}, Fields: []ir.FieldIR{
				{ID: id, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}},
				{ID: title, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeString}, Nullable: true}},
				{ID: relation, Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{}},
			}, PrimaryKey: &ir.KeyIR{ID: keyID, Kind: ir.KeyPrimary, Fields: []ir.FieldID{id}},
		}}},
		Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{{
			ModelID: modelID, GraphQLName: "Post", Exposed: true, Fields: []ir.FieldContractIR{
				{FieldID: id, GraphQLName: "id", Modes: []ir.FieldMode{ir.ModeVisible}},
				{FieldID: title, GraphQLName: "title", Modes: []ir.FieldMode{ir.ModeVisible}},
				{FieldID: relation, GraphQLName: "author", Modes: []ir.FieldMode{ir.ModeVisible}},
			},
		}}},
	}
	enabled := true
	diagnostics := Normalize(&compilation, []ModelPatch{{ModelID: modelID, Subscriptions: &enabled}})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	model := compilation.Contract.Models[0]
	if compilation.Contract.GraphQLABIVersion != ABIVersion || !model.Subscriptions || model.Roots.Events != "postEvents" || model.Event == nil {
		t.Fatalf("normalized subscription = %#v", model)
	}
	if model.Event.PayloadTypeName != "PostEvent" || model.Event.IdentityTypeName != "" || !model.Event.DeleteSnapshotFull || len(model.Event.Schema.IdentityFields) != 1 || model.Event.Schema.IdentityFields[0].FieldID != id {
		t.Fatalf("normalized event identity = %#v", model.Event)
	}
	if got := model.Event.Schema.SnapshotFields; len(got) != 2 || got[0].FieldID != id || got[1].FieldID != title {
		t.Fatalf("stored-scalar snapshot order = %#v", got)
	}
	wantMetadata := []string{"eventID", "type", "id", "entity", "causationID", "transactionOrdinal", "recordedAt"}
	if !reflect.DeepEqual(model.Event.MetadataFields, wantMetadata) || model.Event.SchemaFingerprint == "" || model.Limits.DefaultPageSize != DefaultPageSize || model.Limits.MaxPageSize != DefaultMaxPageSize {
		t.Fatalf("event metadata/limits = %#v / %#v", model.Event, model.Limits)
	}
}

func TestP7ContractRejectsHiddenKeylessCollidingAndUncapturableModels(t *testing.T) {
	makeModel := func(modelID ir.ModelID, key bool, mode ir.FieldMode) (ir.ModelDeclIR, ir.ModelContractIR) {
		fieldID := ir.FieldID(string(modelID)[:31] + "f")
		logical := ir.ModelDeclIR{ID: modelID, Fields: []ir.FieldIR{{ID: fieldID, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}}}}
		if key {
			logical.PrimaryKey = &ir.KeyIR{ID: ir.KeyID(string(modelID)[:31] + "k"), Kind: ir.KeyPrimary, Fields: []ir.FieldID{fieldID}}
		}
		contract := ir.ModelContractIR{ModelID: modelID, GraphQLName: "Post", Exposed: true, Fields: []ir.FieldContractIR{{FieldID: fieldID, GraphQLName: "id", Modes: []ir.FieldMode{mode}}}}
		return logical, contract
	}
	enabled := true
	for name, setup := range map[string]func(*ir.CompilationIR){
		"hidden-model": func(value *ir.CompilationIR) { value.Contract.Models[0].Exposed = false },
		"keyless":      func(value *ir.CompilationIR) { value.Model.Models[0].PrimaryKey = nil },
		"hidden-key": func(value *ir.CompilationIR) {
			value.Contract.Models[0].Fields[0].Modes = []ir.FieldMode{ir.ModeHidden}
		},
		"uncapturable": func(value *ir.CompilationIR) {
			value.Model.Models[0].Fields[0].Scalar.Type.Kind = "futureOpaque"
		},
	} {
		t.Run(name, func(t *testing.T) {
			logical, contract := makeModel("10000000000000000000000000000000", true, ir.ModeVisible)
			compilation := ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{logical}}, Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{contract}}}
			setup(&compilation)
			if diagnostics := Normalize(&compilation, []ModelPatch{{ModelID: contract.ModelID, Subscriptions: &enabled}}); len(diagnostics) == 0 {
				t.Fatal("invalid subscription contract was accepted")
			}
		})
	}
	leftLogical, leftContract := makeModel("10000000000000000000000000000000", true, ir.ModeVisible)
	rightLogical, rightContract := makeModel("20000000000000000000000000000000", true, ir.ModeVisible)
	rightContract.GraphQLName = "User"
	compilation := ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{leftLogical, rightLogical}}, Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{leftContract, rightContract}}}
	root := ir.GraphQLRootNamesIR{Events: "events"}
	diagnostics := Normalize(&compilation, []ModelPatch{{ModelID: leftContract.ModelID, Subscriptions: &enabled, Roots: &root}, {ModelID: rightContract.ModelID, Subscriptions: &enabled, Roots: &root}})
	found := false
	for _, diagnostic := range diagnostics {
		found = found || diagnostic.Code == "P7_EVENT_ROOT_COLLISION"
	}
	if !found {
		t.Fatalf("missing event-root collision: %#v", diagnostics)
	}
	logical, contract := makeModel("30000000000000000000000000000000", true, ir.ModeVisible)
	enumCollision := ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{logical}}, Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{contract}, Enums: []ir.EnumContractIR{{EnumID: "3000000000000000000000000000000e", GraphQLName: "GolemEventType", Values: []ir.EnumValueContractIR{{ValueID: "3000000000000000000000000000000f", GraphQLName: "VALUE"}}}}}}
	diagnostics = Normalize(&enumCollision, []ModelPatch{{ModelID: contract.ModelID, Subscriptions: &enabled}})
	found = false
	for _, diagnostic := range diagnostics {
		found = found || diagnostic.Code == "P5_GRAPHQL_TYPE_COLLISION"
	}
	if !found {
		t.Fatalf("missing shared event-enum collision: %#v", diagnostics)
	}
}

func TestP7EventSchemaDigestIgnoresGraphQLOnlyChangesButTracksLogicalEventShape(t *testing.T) {
	id := ir.FieldID("10000000000000000000000000000001")
	modelID := ir.ModelID("10000000000000000000000000000010")
	logical := ir.ModelDeclIR{ID: modelID, Fields: []ir.FieldIR{{ID: id, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}}}, PrimaryKey: &ir.KeyIR{ID: "10000000000000000000000000000011", Kind: ir.KeyPrimary, Fields: []ir.FieldID{id}}}
	contract := ir.ModelContractIR{ModelID: modelID, GraphQLName: "Post", Exposed: true, Fields: []ir.FieldContractIR{{FieldID: id, GraphQLName: "id", Modes: []ir.FieldMode{ir.ModeVisible}}}}
	enabled := true
	compile := func(logical ir.ModelDeclIR, contract ir.ModelContractIR, root string) ir.CompilationIR {
		value := ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{logical}}, Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{contract}}}
		roots := ir.GraphQLRootNamesIR{Events: root}
		if diagnostics := Normalize(&value, []ModelPatch{{ModelID: modelID, Subscriptions: &enabled, Roots: &roots}}); len(diagnostics) != 0 {
			t.Fatalf("normalize: %#v", diagnostics)
		}
		return value
	}
	left := compile(logical, contract, "postEvents")
	renamed := contract
	renamed.GraphQLName = "Article"
	renamed.Fields = append([]ir.FieldContractIR(nil), contract.Fields...)
	renamed.Fields[0].GraphQLName = "identifier"
	right := compile(logical, renamed, "articleChanges")
	if left.Contract.Models[0].Event.SchemaFingerprint != right.Contract.Models[0].Event.SchemaFingerprint {
		t.Fatal("GraphQL-only names changed event schema fingerprint")
	}
	right.Contract.Models[0].Event.MetadataFields[0] = "renamedEventID"
	recomputed, err := ir.EventSchemaFingerprint(right.Contract.Models[0].Event.Schema)
	if err != nil || recomputed != left.Contract.Models[0].Event.SchemaFingerprint {
		t.Fatal("GraphQL-only public metadata changed event schema fingerprint")
	}
	leftContract, _ := ir.ContractFingerprint(left.Contract)
	rightContract, _ := ir.ContractFingerprint(right.Contract)
	if leftContract == rightContract {
		t.Fatal("GraphQL-only names did not change ContractFingerprint")
	}
	changedLogical := logical
	changedLogical.Fields = append([]ir.FieldIR(nil), logical.Fields...)
	changedLogical.Fields[0].Scalar = &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeString}}
	changed := compile(changedLogical, contract, "postEvents")
	if left.Contract.Models[0].Event.SchemaFingerprint == changed.Contract.Models[0].Event.SchemaFingerprint {
		t.Fatal("logical event value shape did not change event schema fingerprint")
	}
}
