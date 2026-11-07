package contract

import (
	"reflect"
	"strings"
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

func TestContractNormalizesSubscriptionsRootsIdentitiesSnapshotsAndLimits(t *testing.T) {
	id := ir.FieldID("10000000000000000000000000000001")
	title := ir.FieldID("10000000000000000000000000000002")
	relation := ir.FieldID("10000000000000000000000000000003")
	author := ir.FieldID("10000000000000000000000000000004")
	body := ir.FieldID("10000000000000000000000000000005")
	payload := ir.FieldID("10000000000000000000000000000006")
	wide := ir.FieldID("10000000000000000000000000000007")
	tags := ir.FieldID("10000000000000000000000000000008")
	modelID := ir.ModelID("10000000000000000000000000000010")
	keyID := ir.KeyID("10000000000000000000000000000011")
	short, long := uint32(160), uint32(2000)
	compilation := func() ir.CompilationIR {
		return ir.CompilationIR{
			Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{{
				ID: modelID, Go: ir.GoNamedTypeIR{Name: "Post"}, Fields: []ir.FieldIR{
					{ID: id, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}},
					{ID: title, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeString, MaxLength: &short}, Nullable: true}},
					{ID: relation, Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{}},
					{ID: author, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}},
					{ID: body, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeString}}},
					{ID: payload, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeJSON}}},
					{ID: wide, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeString, MaxLength: &long}}},
					{ID: tags, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeScalarList, Element: &ir.LogicalTypeIR{Kind: ir.TypeInt32}}}},
				}, PrimaryKey: &ir.KeyIR{ID: keyID, Kind: ir.KeyPrimary, Fields: []ir.FieldID{id}},
			}}, Relations: []ir.RelationIR{{SourceModel: modelID, TargetModel: "10000000000000000000000000000020", SourceField: relation, Cardinality: ir.RelationOne, LocalFields: []ir.FieldID{author}}}},
			Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{{
				ModelID: modelID, GraphQLName: "Post", Exposed: true, Fields: []ir.FieldContractIR{
					{FieldID: id, GraphQLName: "id", Modes: []ir.FieldMode{ir.ModeVisible}},
					{FieldID: title, GraphQLName: "title", Modes: []ir.FieldMode{ir.ModeVisible}},
					{FieldID: relation, GraphQLName: "author", Modes: []ir.FieldMode{ir.ModeVisible}},
				},
			}}},
		}
	}
	enabled := true
	snapshotOf := func(t *testing.T, declared *[]ir.FieldID) ir.EventContractIR {
		t.Helper()
		value := compilation()
		if diagnostics := Normalize(&value, []ModelPatch{{ModelID: modelID, Subscriptions: &enabled, DeleteSnapshot: declared}}); len(diagnostics) != 0 {
			t.Fatalf("diagnostics = %#v", diagnostics)
		}
		model := value.Contract.Models[0]
		if value.Contract.GraphQLABIVersion != ABIVersion || !model.Subscriptions || model.Roots.Events != "postEvents" || model.Event == nil {
			t.Fatalf("normalized subscription = %#v", model)
		}
		wantMetadata := []string{"eventID", "type", "id", "entity", "causationID", "transactionOrdinal", "recordedAt", "version"}
		if !reflect.DeepEqual(model.Event.MetadataFields, wantMetadata) || model.Event.SchemaFingerprint == "" || model.Limits.DefaultPageSize != DefaultPageSize || model.Limits.MaxPageSize != DefaultMaxPageSize {
			t.Fatalf("event metadata/limits = %#v / %#v", model.Event, model.Limits)
		}
		if model.Event.PayloadTypeName != "PostEvent" || model.Event.IdentityTypeName != "" || len(model.Event.Schema.IdentityFields) != 1 || model.Event.Schema.IdentityFields[0].FieldID != id {
			t.Fatalf("normalized event identity = %#v", model.Event)
		}
		return *model.Event
	}
	fields := func(event ir.EventContractIR) []ir.FieldID {
		result := make([]ir.FieldID, len(event.Schema.SnapshotFields))
		for index, field := range event.Schema.SnapshotFields {
			result[index] = field.FieldID
		}
		return result
	}
	defaulted := snapshotOf(t, nil)
	if got := fields(defaulted); !reflect.DeepEqual(got, []ir.FieldID{id, title, author}) || defaulted.DeleteSnapshotFull {
		t.Fatalf("default bounded snapshot = %v full=%v", got, defaulted.DeleteSnapshotFull)
	}
	declared := []ir.FieldID{body}
	explicit := snapshotOf(t, &declared)
	if got := fields(explicit); !reflect.DeepEqual(got, []ir.FieldID{id, author, body}) || explicit.DeleteSnapshotFull {
		t.Fatalf("declared snapshot = %v full=%v", got, explicit.DeleteSnapshotFull)
	}
	if explicit.SchemaFingerprint == defaulted.SchemaFingerprint {
		t.Fatal("declared and default snapshots share a fingerprint")
	}
	everything := []ir.FieldID{tags, wide, payload, body, title}
	full := snapshotOf(t, &everything)
	if got := fields(full); !reflect.DeepEqual(got, []ir.FieldID{id, title, author, body, payload, wide, tags}) || !full.DeleteSnapshotFull {
		t.Fatalf("full declared snapshot = %v full=%v", got, full.DeleteSnapshotFull)
	}
	foreign := []ir.FieldID{relation}
	value := compilation()
	if diagnostics := Normalize(&value, []ModelPatch{{ModelID: modelID, Subscriptions: &enabled, DeleteSnapshot: &foreign}}); !hasDiagnostic(diagnostics, "P7_DELETE_SNAPSHOT_FIELD") {
		t.Fatalf("relation snapshot diagnostics = %#v", diagnostics)
	}
}

func TestContractRejectsKeylessCollidingAndUncapturableModels(t *testing.T) {
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
		"keyless": func(value *ir.CompilationIR) { value.Model.Models[0].PrimaryKey = nil },
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
	hiddenLogical, hiddenContract := makeModel("10000000000000000000000000000000", true, ir.ModeVisible)
	hiddenContract.Exposed = false
	hidden := ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{hiddenLogical}}, Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{hiddenContract}}}
	if diagnostics := Normalize(&hidden, []ModelPatch{{ModelID: hiddenContract.ModelID, Subscriptions: &enabled}}); len(diagnostics) != 0 {
		t.Fatalf("GraphQL-hidden subscribed model was rejected: %#v", diagnostics)
	}
	if model := hidden.Contract.Models[0]; model.Exposed || !model.Subscriptions || model.Event == nil {
		t.Fatalf("GraphQL-hidden subscribed model contract=%#v", model)
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

func TestEventSchemaDigestIgnoresGraphQLOnlyChangesButTracksLogicalEventShape(t *testing.T) {
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

func listBoundCompilation(result ir.GraphQLTypeIR, arguments []ir.GraphQLArgumentContractIR, computed ir.GraphQLTypeIR) ir.CompilationIR {
	modelID := ir.ModelID("10000000000000000000000000000000")
	model := ir.ModelContractIR{
		ModelID: modelID, GraphQLName: "Post", Exposed: true,
		Fields: []ir.FieldContractIR{{FieldID: "20000000000000000000000000000000", GraphQLName: "id", Modes: []ir.FieldMode{ir.ModeVisible}}},
	}
	if computed.Kind != "" {
		model.Computed = []ir.ComputedFieldContractIR{{ExtensionID: "ext-related", Name: "related", Result: computed}}
	}
	return ir.CompilationIR{Contract: ir.ContractIR{
		FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{model},
		CustomOperations: []ir.CustomOperationContractIR{{
			ExtensionID: "ext-search", Operation: ir.CustomOperationQuery, Name: "search", Arguments: arguments, Result: result,
			Resolver: ir.AttachedMethodIR{PackagePath: "example/app", Name: "Search", Kind: "customQuery"}, Capability: ir.CustomOperationCallerOnly,
		}},
	}}
}

func postList(maxItems uint32) ir.GraphQLTypeIR {
	return ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, MaxItems: maxItems, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: "Post"}}
}

func TestNormalizeRejectsListBoundsMissingOrMisplacedInAHandBuiltContract(t *testing.T) {
	stringList := func(maxItems uint32) []ir.GraphQLArgumentContractIR {
		return []ir.GraphQLArgumentContractIR{{Name: "ids", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, MaxItems: maxItems, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}}}}
	}
	cases := []struct {
		name        string
		compilation ir.CompilationIR
		code        string
		mention     string
	}{
		{"custom result without a maximum", listBoundCompilation(postList(0), nil, ir.GraphQLTypeIR{}), "P5_GRAPHQL_LIST_MAX_REQUIRED", "custom query search result list level 1"},
		{"custom nested result with an unbounded inner level", listBoundCompilation(ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, MaxItems: 4, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: "Post"}}}, nil, ir.GraphQLTypeIR{}), "P5_GRAPHQL_LIST_MAX_REQUIRED", "result list level 2"},
		{"computed result without a maximum", listBoundCompilation(ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}, nil, postList(0)), "P5_GRAPHQL_LIST_MAX_REQUIRED", "computed field Post.related result list level 1"},
		{"maximum on an argument list", listBoundCompilation(ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}, stringList(5), ir.GraphQLTypeIR{}), "P5_GRAPHQL_LIST_MAX_SCALAR", "custom query search argument ids"},
		{"maximum on a non-list result", listBoundCompilation(ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", MaxItems: 3}, nil, ir.GraphQLTypeIR{}), "P5_GRAPHQL_LIST_MAX_SCALAR", "custom query search result level 1"},
		{"maximum on a list element", listBoundCompilation(ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, MaxItems: 2, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: "Post", MaxItems: 9}}, nil, ir.GraphQLTypeIR{}), "P5_GRAPHQL_LIST_MAX_SCALAR", "result level 2"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			compilation := test.compilation
			found := false
			for _, diagnostic := range Normalize(&compilation, nil) {
				if diagnostic.Code == test.code && strings.Contains(diagnostic.Message, test.mention) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s mentioning %q", test.code, test.mention)
			}
		})
	}
}

func TestNormalizeAcceptsDeclaredListBoundsAtEveryLevel(t *testing.T) {
	nested := ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, MaxItems: 10, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, MaxItems: 5, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: "Post"}}}
	compilation := listBoundCompilation(nested, []ir.GraphQLArgumentContractIR{{Name: "ids", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}}}}, postList(7))
	if diagnostics := Normalize(&compilation, nil); len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestInternalRequiredFieldNeedsANonClientOwnerWhenCreateIsExposed(t *testing.T) {
	modelID := ir.ModelID("10000000000000000000000000000030")
	keyField := ir.FieldID("10000000000000000000000000000031")
	hashField := ir.FieldID("10000000000000000000000000000032")
	build := func(modes []ir.FieldMode, operations []ir.Operation, hashScalar ir.ScalarFieldIR) ir.CompilationIR {
		return ir.CompilationIR{
			Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{{ID: modelID, Fields: []ir.FieldIR{
				{ID: keyField, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}},
				{ID: hashField, Kind: ir.FieldScalar, Scalar: &hashScalar},
			}, PrimaryKey: &ir.KeyIR{ID: "k", Kind: ir.KeyPrimary, Fields: []ir.FieldID{keyField}}}}},
			Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{{
				ModelID: modelID, GraphQLName: "Key", Exposed: true, Operations: operations,
				Fields: []ir.FieldContractIR{{FieldID: keyField, GraphQLName: "id", Modes: []ir.FieldMode{ir.ModeVisible}}, {FieldID: hashField, GraphQLName: "keyHash", Modes: modes}},
			}}},
		}
	}
	required := ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeString}}
	create := []ir.Operation{ir.OperationFindOne, ir.OperationCreate}
	if diagnostics := ValidateInternalFields(build([]ir.FieldMode{ir.ModeInternal}, create, required)); !hasDiagnostic(diagnostics, "P8_GRAPHQL_INTERNAL_CLIENT_REQUIRED") {
		t.Fatalf("unsafe internal field diagnostics=%#v", diagnostics)
	}
	if diagnostics := ValidateInternalFields(build([]ir.FieldMode{ir.ModeInternal, ir.ModeSystem}, create, required)); len(diagnostics) != 0 {
		t.Fatalf("system internal field diagnostics=%#v", diagnostics)
	}
	if diagnostics := ValidateInternalFields(build([]ir.FieldMode{ir.ModeInternal}, []ir.Operation{ir.OperationFindOne}, required)); len(diagnostics) != 0 {
		t.Fatalf("no create root diagnostics=%#v", diagnostics)
	}
	nullable := required
	nullable.Nullable = true
	if diagnostics := ValidateInternalFields(build([]ir.FieldMode{ir.ModeInternal}, create, nullable)); len(diagnostics) != 0 {
		t.Fatalf("nullable internal field diagnostics=%#v", diagnostics)
	}
	defaulted := required
	defaulted.Default = &ir.DefaultIR{}
	if diagnostics := ValidateInternalFields(build([]ir.FieldMode{ir.ModeInternal}, create, defaulted)); len(diagnostics) != 0 {
		t.Fatalf("defaulted internal field diagnostics=%#v", diagnostics)
	}
}
