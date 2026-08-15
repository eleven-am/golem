package schema

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestSocialSDLIsDeterministicAndValid(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	first, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	if first.SDL != second.SDL {
		t.Fatal("SDL generation is not deterministic")
	}
	if _, err := gqlparser.LoadSchema(astSource(first.SDL)); err != nil {
		t.Fatalf("invalid generated SDL: %v\n%s", err, first.SDL)
	}
	for _, expected := range []string{"type User", "type Post", "input CommentWhereInput", "input PostUpdateInput", "createPost", "updateManyPosts", "comments(where:"} {
		if !strings.Contains(first.SDL, expected) {
			t.Errorf("SDL missing %q", expected)
		}
	}
}

func TestSemanticIndexAddsGeneratedSearchQuery(t *testing.T) {
	compilation := compilerir.CompilationIR{
		Model: compilerir.ModelIR{Schema: compilerir.SchemaIdentityIR{PackagePath: "example.test/app"}, Models: []compilerir.ModelDeclIR{{
			ID: "record", Go: compilerir.GoNamedTypeIR{PackagePath: "example.test/app", Name: "Record"}, LogicalName: "Record",
			Fields:     []compilerir.FieldIR{{ID: "record-id", GoName: "ID", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}}}},
			PrimaryKey: &compilerir.KeyIR{ID: "record-primary", Kind: compilerir.KeyPrimary, Fields: []compilerir.FieldID{"record-id"}},
		}}},
		Contract: compilerir.ContractIR{GraphQLABIVersion: 5, Models: []compilerir.ModelContractIR{{
			ModelID: "record", GraphQLName: "Record", GraphQLPlural: "Records", Exposed: true, Limits: compilerir.LimitContractIR{DefaultPageSize: 25},
			Fields: []compilerir.FieldContractIR{{FieldID: "record-id", GraphQLName: "id"}}, Selectors: []compilerir.SelectorContractIR{{KeyID: "record-primary", Kind: compilerir.KeyPrimary, Name: "primary", Fields: []compilerir.FieldID{"record-id"}}},
		}}},
	}
	payload, _ := semanticcontract.Encode(semanticcontract.Index{Name: "content", Space: "content", Dimensions: 3, Fields: []string{"field"}, Metric: "cosine"})
	compilation.Model.Extensions = []compilerir.ProviderExtensionIR{{ID: "index", Provider: compilerir.SQLite, Version: 1, Owner: "record", Kind: semanticcontract.IndexKind, Payload: payload}}
	if diagnostics := graphqlextension.AddSemanticSearchOperations(&compilation); len(diagnostics) != 0 {
		t.Fatalf("semantic diagnostics = %#v", diagnostics)
	}
	document, err := Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	want := "searchRecordsByContent(query: String!, take: Int, where: RecordWhereInput): [Record!]!"
	if !strings.Contains(document.SDL, want) {
		t.Fatalf("semantic SDL missing %q:\n%s", want, document.SDL)
	}
}

func TestGraphQLHookOwnedOmitsScalarAndCanonicalRelationFromEveryCreateShape(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	var postModel *compilerir.ModelDeclIR
	var postContract *compilerir.ModelContractIR
	for index := range compilation.Contract.Models {
		if compilation.Contract.Models[index].GraphQLName == "Post" {
			postContract = &compilation.Contract.Models[index]
			break
		}
	}
	if postContract == nil {
		t.Fatal("Post contract is absent")
	}
	for index := range compilation.Model.Models {
		if compilation.Model.Models[index].ID == postContract.ModelID {
			postModel = &compilation.Model.Models[index]
			break
		}
	}
	if postModel == nil {
		t.Fatal("Post model is absent")
	}
	var authorID, author compilerir.FieldID
	for index := range postModel.Fields {
		field := &postModel.Fields[index]
		switch field.GoName {
		case "AuthorID":
			authorID = field.ID
		case "Author":
			author = field.ID
		}
	}
	if authorID == "" || author == "" {
		t.Fatal("Post author fields are absent")
	}
	postContract.HookOwnedCreateFields = []compilerir.FieldID{authorID}
	for index := range postContract.Fields {
		if postContract.Fields[index].FieldID == authorID || postContract.Fields[index].FieldID == author {
			postContract.Fields[index].Modes = append(postContract.Fields[index].Modes, compilerir.ModeImmutable)
		}
	}
	document, err := Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatalf("invalid hook-owned SDL: %v\n%s", err, document.SDL)
	}
	for name, definition := range parsed.Types {
		if definition.Kind != ast.InputObject || !strings.HasPrefix(name, "PostCreate") {
			continue
		}
		if definition.Fields.ForName("authorID") != nil || definition.Fields.ForName("author") != nil {
			t.Fatalf("hook-owned author leaked into %s: %#v", name, definition.Fields)
		}
	}
	postCreate := requireDefinition(t, parsed, "PostCreateInput")
	if postCreate.Fields.ForName("authorID") != nil || postCreate.Fields.ForName("author") != nil {
		t.Fatalf("PostCreateInput leaked hook-owned author: %#v", postCreate.Fields)
	}
	postOutput := requireDefinition(t, parsed, "Post")
	postWhere := requireDefinition(t, parsed, "PostWhereInput")
	if postOutput.Fields.ForName("authorID") == nil || postOutput.Fields.ForName("author") == nil || postWhere.Fields.ForName("authorID") == nil || postWhere.Fields.ForName("author") == nil {
		t.Fatal("hook ownership changed normal output/filter exposure")
	}
	if update := parsed.Types["PostUpdateInput"]; update != nil && (update.Fields.ForName("authorID") != nil || update.Fields.ForName("author") != nil) {
		t.Fatalf("immutable author leaked into PostUpdateInput: %#v", update.Fields)
	}
}

func TestGraphQLHookOwnedRefusesAuthoredRootWithNoClientCreatePosition(t *testing.T) {
	modelID := compilerir.ModelID("10000000000000000000000000000001")
	fieldID := compilerir.FieldID("10000000000000000000000000000002")
	model := compilerir.ModelDeclIR{ID: modelID, Fields: []compilerir.FieldIR{{
		ID: fieldID, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}},
	}}}
	contract := compilerir.ModelContractIR{
		ModelID: modelID, GraphQLName: "Tenant", Exposed: true,
		Fields:                []compilerir.FieldContractIR{{FieldID: fieldID, GraphQLName: "slug"}},
		Operations:            []compilerir.Operation{compilerir.OperationFindOne, compilerir.OperationCreate},
		Roots:                 compilerir.GraphQLRootNamesIR{FindOne: "tenant", Create: "createTenant"},
		HookOwnedCreateFields: []compilerir.FieldID{fieldID},
	}
	view := modelView{model: model, contract: contract, fields: map[compilerir.FieldID]compilerir.FieldContractIR{fieldID: contract.Fields[0]}, relations: map[compilerir.RelationID]compilerir.RelationIR{}}
	if _, _, _, err := renderRoots([]modelView{view}, nil); err == nil || !strings.Contains(err.Error(), "P8_GRAPHQL_HOOK_OWNED_EMPTY_CREATE") {
		t.Fatalf("all-server-owned create root error = %v", err)
	}

	defaulted := *model.Fields[0].Scalar
	defaulted.Default = &compilerir.DefaultIR{Kind: compilerir.DefaultLiteral}
	model.Fields[0].Scalar = &defaulted
	contract.HookOwnedCreateFields = nil
	view = modelView{model: model, contract: contract, fields: map[compilerir.FieldID]compilerir.FieldContractIR{fieldID: contract.Fields[0]}, relations: map[compilerir.RelationID]compilerir.RelationIR{}}
	_, mutation, _, err := renderRoots([]modelView{view}, nil)
	if err != nil {
		t.Fatalf("defaulted create position refused: %v", err)
	}
	if !strings.Contains(mutation, "createTenant(data: TenantCreateInput!)") {
		t.Fatalf("defaulted create root missing: %s", mutation)
	}
}

func TestGeneratedSubscriptionSDLMatchesClosedEventABIGolden(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	for index := range compiled.Compilation.Contract.Models {
		contract := &compiled.Compilation.Contract.Models[index]
		if contract.GraphQLName != "Post" && contract.GraphQLName != "Friendship" {
			continue
		}
		contract.Subscriptions = true
		if contract.GraphQLName == "Post" {
			contract.Roots.Events = "postEvents"
		} else {
			contract.Roots.Events = "friendshipEvents"
		}
		var logical *compilerir.ModelDeclIR
		for modelIndex := range compiled.Compilation.Model.Models {
			if compiled.Compilation.Model.Models[modelIndex].ID == contract.ModelID {
				logical = &compiled.Compilation.Model.Models[modelIndex]
				break
			}
		}
		if logical == nil || logical.PrimaryKey == nil {
			t.Fatal("Post logical model or primary key is absent")
		}
		shape, err := compilerir.BuildEventSchemaShape(*logical, compiled.Compilation.Model.Enums, logical.PrimaryKey.Fields)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := compilerir.EventSchemaFingerprint(shape)
		if err != nil {
			t.Fatal(err)
		}
		identityType := ""
		if len(shape.IdentityFields) > 1 {
			identityType = contract.GraphQLName + "EventIdentity"
		}
		contract.Event = &compilerir.EventContractIR{PayloadTypeName: contract.GraphQLName + "Event", IdentityTypeName: identityType, MetadataFields: []string{"eventID", "type", "id", "entity", "causationID", "transactionOrdinal", "recordedAt"}, Schema: shape, SchemaFingerprint: fingerprint}
	}
	document, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatalf("invalid event SDL: %v\n%s", err, document.SDL)
	}
	eventType := requireDefinition(t, parsed, "GolemEventType")
	var eventValues []string
	for _, value := range eventType.EnumValues {
		eventValues = append(eventValues, value.Name)
	}
	if strings.Join(eventValues, ",") != "CREATED,UPDATED,DELETED" {
		t.Fatalf("event enum = %v", eventType.EnumValues)
	}
	event := requireDefinition(t, parsed, "PostEvent")
	requireFieldNames(t, event, "eventID", "causationID", "transactionOrdinal", "recordedAt", "type", "id", "entity")
	if event.Fields.ForName("entity").Type.String() != "Post" || event.Fields.ForName("id").Type.String() != "UUID!" {
		t.Fatalf("event field types = entity %s / id %s", event.Fields.ForName("entity").Type, event.Fields.ForName("id").Type)
	}
	root := requireDefinition(t, parsed, "Subscription").Fields.ForName("postEvents")
	if root == nil || root.Type.String() != "PostEvent!" || root.Arguments.ForName("where") == nil || root.Arguments.ForName("where").Type.String() != "PostWhereInput" {
		t.Fatalf("subscription root = %#v", root)
	}
	if strings.Count(document.SDL, "enum GolemEventType") != 1 || strings.Count(document.SDL, "type Subscription") != 1 {
		t.Fatal("shared event enum/root were emitted more than once")
	}
	friendshipIdentity := requireDefinition(t, parsed, "FriendshipEventIdentity")
	if len(friendshipIdentity.Fields) != 2 || requireDefinition(t, parsed, "Subscription").Fields.ForName("friendshipEvents") == nil {
		t.Fatalf("compound event identity/root = %#v", friendshipIdentity)
	}
}

func TestSubscriptionDoesNotManufactureMissingQueryRoot(t *testing.T) {
	compilation := compilerir.CompilationIR{Contract: compilerir.ContractIR{GraphQLABIVersion: 1}}
	if _, err := Build(compilation); err == nil || !strings.Contains(err.Error(), "at least one enabled query root") {
		t.Fatalf("subscription/no-query schema error = %v", err)
	}
}

func TestGeneratedGraphQLAnalyticsSDLGolden(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := analyticsSocialCompilation(t, *compiled.Compilation)
	document, err := Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatalf("invalid analytics SDL: %v\n%s", err, document.SDL)
	}
	query := requireDefinition(t, parsed, "Query")
	goldenRoots := map[string]string{
		"aggregatePosts":       "PostAggregate!",
		"groupByPosts":         "[PostGroup!]!",
		"relationGroupByPosts": "[PostRelationGroup!]!",
	}
	for name, expected := range goldenRoots {
		field := query.Fields.ForName(name)
		if field == nil || field.Type.String() != expected {
			t.Errorf("%s type = %#v, want %s", name, field, expected)
		}
	}
	requireFieldNames(t, requireDefinition(t, parsed, "PostAggregate"), "count", "countFields", "max", "min")
	requireFieldNames(t, requireDefinition(t, parsed, "PostCountAggregate"), "title")
	if parsed.Types["PostSumAggregate"] != nil || parsed.Types["PostAvgAggregate"] != nil {
		t.Fatal("unsupported string sum/average capability was emitted")
	}
	requireFieldNames(t, requireDefinition(t, parsed, "PostGroupKey"), "authorID", "title")
	requireFieldNames(t, requireDefinition(t, parsed, "PostRelationGroupKey"), "authorHandle", "authorID", "title")
	if requireDefinition(t, parsed, "PostMinAggregate").Fields.ForName("authorID") != nil {
		t.Fatal("UUID min capability was emitted")
	}

	ordinary := socialDocument(t)
	for _, reserved := range []string{"aggregatePosts", "groupByPosts", "relationGroupByPosts"} {
		if strings.Contains(ordinary.SDL, reserved+"(") {
			t.Fatalf("ordinary P5 schema exposed reserved analytics root %s", reserved)
		}
	}
}

func TestGraphQLAnalyticsRejectsHiddenAllowlistsTerminalsAndNameCollisions(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	for name, mutate := range map[string]func(*compilerir.CompilationIR){
		"hidden allowlisted local field": func(compilation *compilerir.CompilationIR) {
			for modelIndex := range compilation.Contract.Models {
				if compilation.Contract.Models[modelIndex].GraphQLName != "Post" {
					continue
				}
				for fieldIndex := range compilation.Contract.Models[modelIndex].Fields {
					if compilation.Contract.Models[modelIndex].Fields[fieldIndex].GraphQLName == "title" {
						compilation.Contract.Models[modelIndex].Fields[fieldIndex].Modes = []compilerir.FieldMode{compilerir.ModeHidden}
					}
				}
			}
		},
		"hidden relation terminal": func(compilation *compilerir.CompilationIR) {
			for modelIndex := range compilation.Contract.Models {
				if compilation.Contract.Models[modelIndex].GraphQLName != "User" {
					continue
				}
				for fieldIndex := range compilation.Contract.Models[modelIndex].Fields {
					if compilation.Contract.Models[modelIndex].Fields[fieldIndex].GraphQLName == "handle" {
						compilation.Contract.Models[modelIndex].Fields[fieldIndex].Modes = []compilerir.FieldMode{compilerir.ModeHidden}
					}
				}
			}
		},
		"relation name collides with local field": func(compilation *compilerir.CompilationIR) {
			for index := range compilation.Contract.Models {
				contract := &compilation.Contract.Models[index]
				if contract.GraphQLName == "Post" {
					contract.Aggregation.RelationDimensions[0].Name = "title"
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			compilation := analyticsSocialCompilation(t, *compiled.Compilation)
			for index := range compilation.Contract.Models {
				compilation.Contract.Models[index].Fields = append([]compilerir.FieldContractIR(nil), compilation.Contract.Models[index].Fields...)
				if compilation.Contract.Models[index].Aggregation != nil {
					copy := *compilation.Contract.Models[index].Aggregation
					copy.RelationDimensions = append([]compilerir.RelationDimensionContractIR(nil), copy.RelationDimensions...)
					compilation.Contract.Models[index].Aggregation = &copy
				}
			}
			mutate(&compilation)
			if _, err := Build(compilation); err == nil {
				t.Fatal("invalid analytics GraphQL contract was accepted")
			}
		})
	}
}

func analyticsSocialCompilation(t *testing.T, compilation compilerir.CompilationIR) compilerir.CompilationIR {
	t.Helper()
	compilation.Contract.Models = append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	var postModel, userModel compilerir.ModelDeclIR
	for _, model := range compilation.Model.Models {
		for _, contract := range compilation.Contract.Models {
			if contract.ModelID != model.ID {
				continue
			}
			switch contract.GraphQLName {
			case "Post":
				postModel = model
			case "User":
				userModel = model
			}
		}
	}
	fieldID := func(model compilerir.ModelDeclIR, contract compilerir.ModelContractIR, name string) compilerir.FieldID {
		for _, field := range contract.Fields {
			if field.GraphQLName == name {
				return field.FieldID
			}
		}
		t.Fatalf("missing %s.%s", contract.GraphQLName, name)
		return ""
	}
	var postContract, userContract *compilerir.ModelContractIR
	for index := range compilation.Contract.Models {
		switch compilation.Contract.Models[index].GraphQLName {
		case "Post":
			postContract = &compilation.Contract.Models[index]
		case "User":
			userContract = &compilation.Contract.Models[index]
		}
	}
	if postContract == nil || userContract == nil {
		t.Fatal("social Post/User contract is absent")
	}
	var authorRelation compilerir.RelationID
	for _, relation := range compilation.Model.Relations {
		if relation.SourceModel == postModel.ID && relation.TargetModel == userModel.ID {
			authorRelation = relation.ID
			break
		}
	}
	if authorRelation == "" {
		t.Fatal("Post.author relation is absent")
	}
	postContract.Operations = append(postContract.Operations, compilerir.OperationAggregate, compilerir.OperationGroupBy, compilerir.OperationRelationGroupBy)
	postContract.Aggregation = &compilerir.AggregationContractIR{
		Enabled:    true,
		Dimensions: []compilerir.FieldID{fieldID(postModel, *postContract, "authorID"), fieldID(postModel, *postContract, "title")}, DimensionsExplicit: true,
		Measures: []compilerir.FieldID{fieldID(postModel, *postContract, "title")}, MeasuresExplicit: true,
		RelationDimensions: []compilerir.RelationDimensionContractIR{{Name: "authorHandle", Path: []compilerir.RelationID{authorRelation}, TerminalField: fieldID(userModel, *userContract, "handle")}},
		GraphQLMaxGroups:   100, RelationMaxIntermediateGroups: 10_000,
	}
	return compilation
}

func TestGeneratedGraphQLSDLMatchesExposureOperationAndNullabilityMatrix(t *testing.T) {
	document := socialDocument(t)
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatal(err)
	}

	user := requireDefinition(t, parsed, "User")
	if user.Fields.ForName("email") != nil {
		t.Fatal("hidden User.email was emitted in the GraphQL output")
	}
	for _, fieldName := range []string{"id", "handle", "createdAt", "posts", "comments", "friendshipsFrom", "friendshipsTo", "_count"} {
		field := user.Fields.ForName(fieldName)
		if field == nil {
			t.Errorf("User output omitted exposed field %s", fieldName)
			continue
		}
		if field.Type.NonNull {
			t.Errorf("conditionally maskable User.%s is non-null: %s", fieldName, field.Type.String())
		}
	}

	query := requireDefinition(t, parsed, "Query")
	mutation := requireDefinition(t, parsed, "Mutation")
	for _, rootName := range []string{"user", "users", "post", "posts", "comment", "comments", "friendship", "friendships", "tag", "tags", "postTag", "postTags"} {
		if query.Fields.ForName(rootName) == nil {
			t.Errorf("enabled query root %s was not emitted", rootName)
		}
	}
	for _, rootName := range []string{"createPost", "updatePost", "upsertPost", "deletePost", "updateManyPosts", "deleteManyPosts"} {
		if mutation.Fields.ForName(rootName) == nil {
			t.Errorf("enabled mutation root %s was not emitted", rootName)
		}
	}
	for typeName, definition := range parsed.Types {
		if strings.Contains(typeName, "Aggregate") || strings.Contains(typeName, "GroupBy") || strings.Contains(typeName, "Subscription") {
			t.Errorf("pre-P6/P7 type %s was emitted as %s", typeName, definition.Kind)
		}
	}
	if parsed.Subscription != nil {
		t.Fatal("P5 schema emitted a subscription root before P7")
	}
}

func TestGeneratedGraphQLArtifactsAreByteIdenticalAcrossShuffledInput(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	first, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := *compiled.Compilation
	shuffled.Model.Models = append([]compilerir.ModelDeclIR(nil), shuffled.Model.Models...)
	shuffled.Model.Relations = append([]compilerir.RelationIR(nil), shuffled.Model.Relations...)
	shuffled.Contract.Models = append([]compilerir.ModelContractIR(nil), shuffled.Contract.Models...)
	shuffled.Contract.Enums = append([]compilerir.EnumContractIR(nil), shuffled.Contract.Enums...)
	reverse(shuffled.Model.Models)
	reverse(shuffled.Model.Relations)
	reverse(shuffled.Contract.Models)
	reverse(shuffled.Contract.Enums)
	for index := range shuffled.Model.Models {
		shuffled.Model.Models[index].Fields = append([]compilerir.FieldIR(nil), shuffled.Model.Models[index].Fields...)
		reverse(shuffled.Model.Models[index].Fields)
	}
	for index := range shuffled.Contract.Models {
		shuffled.Contract.Models[index].Fields = append([]compilerir.FieldContractIR(nil), shuffled.Contract.Models[index].Fields...)
		shuffled.Contract.Models[index].Selectors = append([]compilerir.SelectorContractIR(nil), shuffled.Contract.Models[index].Selectors...)
		reverse(shuffled.Contract.Models[index].Fields)
		reverse(shuffled.Contract.Models[index].Selectors)
	}
	second, err := Build(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if first.SDL != second.SDL {
		t.Fatal("SDL changed when unordered IR inventories were reversed")
	}
}

func astSource(value string) *ast.Source { return &ast.Source{Name: "generated.graphql", Input: value} }

func TestGraphQLNestedInputCompileAndSchemaFixturesExposeOnlyLegalCardinalityOperations(t *testing.T) {
	document := socialDocument(t)
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatal(err)
	}

	postUpdate := requireDefinition(t, parsed, "PostUpdateInput")
	postUpdateMany := requireDefinition(t, parsed, "PostUpdateManyInput")
	postCreate := requireDefinition(t, parsed, "PostCreateInput")
	if postCreate.Fields.ForName("authorID") != nil || postUpdate.Fields.ForName("authorID") != nil {
		t.Fatal("source relation-owned Post.authorID is authorable beside the author relation envelope")
	}
	if postUpdateMany.Fields.ForName("authorID") == nil {
		t.Fatal("relation-free PostUpdateManyInput unexpectedly lost authorID")
	}
	for _, relation := range []string{"author", "comments", "postTags"} {
		if postUpdate.Fields.ForName(relation) == nil {
			t.Errorf("PostUpdateInput omitted relation %s", relation)
		}
		if postUpdateMany.Fields.ForName(relation) != nil {
			t.Errorf("PostUpdateManyInput illegally contains relation %s", relation)
		}
	}
	for _, scalar := range []string{"id", "authorID", "title", "body"} {
		if postUpdateMany.Fields.ForName(scalar) == nil {
			t.Errorf("PostUpdateManyInput omitted scalar operation %s", scalar)
		}
	}
	for _, readOnly := range []string{"search", "createdAt"} {
		if postUpdate.Fields.ForName(readOnly) != nil || postUpdateMany.Fields.ForName(readOnly) != nil {
			t.Errorf("database/read-only field %s is writable", readOnly)
		}
	}

	requireFieldNames(t, requireDefinition(t, parsed, "UUIDUpdateOperationsInput"), "set")
	requireFieldNames(t, requireDefinition(t, parsed, "NullableUUIDUpdateOperationsInput"), "set", "setNull")
	requireFieldNames(t, requireDefinition(t, parsed, "BigIntUpdateOperationsInput"), "decrement", "increment", "set")
	if requireDefinition(t, parsed, "UUIDUpdateOperationsInput").Fields.ForName("setNull") != nil {
		t.Fatal("non-null UUID update envelope exposes setNull")
	}

	requireFieldNames(t, requireDefinition(t, parsed, "BooleanFilter"), "equals", "in", "not", "notIn")
	requireFieldNames(t, requireDefinition(t, parsed, "StringFilter"), "contains", "endsWith", "equals", "gt", "gte", "in", "lt", "lte", "mode", "not", "notIn", "startsWith")
	requireFieldNames(t, requireDefinition(t, parsed, "NullableStringFilter"), "contains", "endsWith", "equals", "gt", "gte", "in", "isNull", "lt", "lte", "mode", "not", "notIn", "startsWith")
	requireFieldNames(t, requireDefinition(t, parsed, "JSONFilter"), "arrayContains", "arrayEndsWith", "arrayStartsWith", "equals", "gt", "gte", "lt", "lte", "mode", "not", "path", "stringContains", "stringEndsWith", "stringStartsWith")
	if parsed.Types["BytesListFilter"] != nil || parsed.Types["NullableBytesListFilter"] != nil {
		t.Fatal("SDL exposes scalar-list operators for provider-ineligible Bytes elements")
	}

	author := postCreate.Fields.ForName("author")
	if author == nil || author.Type == nil || !author.Type.NonNull || author.Type.NamedType != "PostAuthorCreateRelationInput" {
		t.Fatalf("required source relation type = %#v", author)
	}
	requireFieldNames(t, requireDefinition(t, parsed, "PostAuthorCreateRelationInput"), "connect", "connectOrCreate", "create")
	requireFieldNames(t, requireDefinition(t, parsed, "PostAuthorUpdateRelationInput"), "connect", "connectOrCreate", "create", "update", "upsert")
	if definition := requireDefinition(t, parsed, "PostAuthorUpdateRelationInput"); definition.Fields.ForName("disconnect") != nil || definition.Fields.ForName("delete") != nil {
		t.Fatal("required source to-one relation exposes disconnect/delete")
	}
	requireFieldNames(t, requireDefinition(t, parsed, "CommentReplyToUpdateRelationInput"), "connect", "connectOrCreate", "create", "delete", "disconnect", "update", "upsert")
	for _, illegal := range []string{"createMany", "set", "updateMany", "deleteMany"} {
		if requireDefinition(t, parsed, "CommentReplyToUpdateRelationInput").Fields.ForName(illegal) != nil {
			t.Errorf("to-one update relation exposes %s", illegal)
		}
	}
	requireFieldNames(t, requireDefinition(t, parsed, "PostCommentsUpdateRelationInput"), "connect", "connectOrCreate", "create", "createMany", "delete", "deleteMany", "disconnect", "set", "update", "updateMany", "upsert")

	commentWithoutPost := requireDefinition(t, parsed, "CommentCreateWithoutPostInput")
	if commentWithoutPost.Fields.ForName("authorID") != nil || commentWithoutPost.Fields.ForName("author") == nil || !commentWithoutPost.Fields.ForName("author").Type.NonNull {
		t.Fatalf("CommentCreateWithoutPost relation ownership = %#v", commentWithoutPost.Fields)
	}
	postTagWithoutPost := requireDefinition(t, parsed, "PostTagCreateWithoutPostInput")
	if postTagWithoutPost.Fields.ForName("tagName") != nil || postTagWithoutPost.Fields.ForName("tag") == nil || !postTagWithoutPost.Fields.ForName("tag").Type.NonNull {
		t.Fatalf("PostTagCreateWithoutPost relation ownership = %#v", postTagWithoutPost.Fields)
	}
}

func TestNestedInputCompileAndSchemaFixturesExposeCompleteSocialWithoutGraph(t *testing.T) {
	document := socialDocument(t)
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatal(err)
	}
	type edge struct {
		prefix, target, back string
		excluded             []string
	}
	edges := []edge{
		{prefix: "UserPosts", target: "Post", back: "Author", excluded: []string{"author", "authorID"}},
		{prefix: "UserComments", target: "Comment", back: "Author", excluded: []string{"author", "authorID"}},
		{prefix: "UserFriendshipsFrom", target: "Friendship", back: "User", excluded: []string{"user", "userID"}},
		{prefix: "UserFriendshipsTo", target: "Friendship", back: "Friend", excluded: []string{"friend", "friendID"}},
		{prefix: "PostAuthor", target: "User", back: "Posts", excluded: []string{"posts"}},
		{prefix: "PostComments", target: "Comment", back: "Post", excluded: []string{"post", "postID"}},
		{prefix: "PostPostTags", target: "PostTag", back: "Post", excluded: []string{"post", "postID"}},
		{prefix: "CommentPost", target: "Post", back: "Comments", excluded: []string{"comments"}},
		{prefix: "CommentAuthor", target: "User", back: "Comments", excluded: []string{"comments"}},
		{prefix: "CommentReplyTo", target: "Comment", back: "Replies", excluded: []string{"replies"}},
		{prefix: "CommentReplies", target: "Comment", back: "ReplyTo", excluded: []string{"replyTo", "parentID"}},
		{prefix: "FriendshipUser", target: "User", back: "FriendshipsFrom", excluded: []string{"friendshipsFrom"}},
		{prefix: "FriendshipFriend", target: "User", back: "FriendshipsTo", excluded: []string{"friendshipsTo"}},
		{prefix: "TagPostTags", target: "PostTag", back: "Tag", excluded: []string{"tag", "tagName"}},
		{prefix: "PostTagPost", target: "Post", back: "PostTags", excluded: []string{"postTags"}},
		{prefix: "PostTagTag", target: "Tag", back: "PostTags", excluded: []string{"postTags"}},
	}
	for _, value := range edges {
		t.Run(value.prefix, func(t *testing.T) {
			createName := value.target + "CreateWithout" + value.back + "Input"
			updateName := value.target + "UpdateWithout" + value.back + "Input"
			create := requireDefinition(t, parsed, createName)
			update := requireDefinition(t, parsed, updateName)
			for _, excluded := range value.excluded {
				if create.Fields.ForName(excluded) != nil || update.Fields.ForName(excluded) != nil {
					t.Errorf("%s/%s retained excluded back-edge field %s", createName, updateName, excluded)
				}
			}

			createRelation := requireDefinition(t, parsed, value.prefix+"CreateRelationInput")
			if field := createRelation.Fields.ForName("create"); field != nil && namedType(field.Type) != createName {
				t.Errorf("create routes to %s, want %s", namedType(field.Type), createName)
			}
			if field := createRelation.Fields.ForName("createMany"); field != nil && namedType(field.Type) != createName {
				t.Errorf("createMany routes to %s, want %s", namedType(field.Type), createName)
			}
			if field := createRelation.Fields.ForName("connectOrCreate"); field != nil {
				helperName := value.target + "ConnectOrCreateWithout" + value.back + "Input"
				if namedType(field.Type) != helperName {
					t.Errorf("connectOrCreate routes to %s, want %s", namedType(field.Type), helperName)
				}
				helper := requireDefinition(t, parsed, helperName)
				if got := namedType(helper.Fields.ForName("create").Type); got != createName {
					t.Errorf("%s.create routes to %s, want %s", helperName, got, createName)
				}
			}

			updateRelation := requireDefinition(t, parsed, value.prefix+"UpdateRelationInput")
			if field := updateRelation.Fields.ForName("create"); field != nil && namedType(field.Type) != createName {
				t.Errorf("nested update create routes to %s, want %s", namedType(field.Type), createName)
			}
			if field := updateRelation.Fields.ForName("update"); field != nil {
				got := namedType(field.Type)
				if strings.Contains(got, "WithWhere") {
					helper := requireDefinition(t, parsed, got)
					got = namedType(helper.Fields.ForName("data").Type)
				}
				if got != updateName {
					t.Errorf("nested update routes to %s, want %s", got, updateName)
				}
			}
			if field := updateRelation.Fields.ForName("upsert"); field != nil {
				helper := requireDefinition(t, parsed, namedType(field.Type))
				if got := namedType(helper.Fields.ForName("create").Type); got != createName {
					t.Errorf("upsert create routes to %s, want %s", got, createName)
				}
				if got := namedType(helper.Fields.ForName("update").Type); got != updateName {
					t.Errorf("upsert update routes to %s, want %s", got, updateName)
				}
			}
		})
	}
}

func TestExcludedRelationTargetFailsGenerationInsteadOfSilentPruning(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	compilation.Contract.Models = append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	for index := range compilation.Contract.Models {
		if compilation.Contract.Models[index].GraphQLName == "User" {
			compilation.Contract.Models[index].Exposed = false
		}
	}
	if _, err := Build(compilation); err == nil || !strings.Contains(err.Error(), "targets hidden model") {
		t.Fatalf("hidden relation target error = %v", err)
	}
}

func socialDocument(t *testing.T) Document {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	document, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func requireDefinition(t *testing.T, schema *ast.Schema, name string) *ast.Definition {
	t.Helper()
	definition := schema.Types[name]
	if definition == nil {
		t.Fatalf("SDL omitted definition %s", name)
	}
	return definition
}

func requireFieldNames(t *testing.T, definition *ast.Definition, expected ...string) {
	t.Helper()
	actual := make([]string, len(definition.Fields))
	for index, field := range definition.Fields {
		actual[index] = field.Name
	}
	sort.Strings(actual)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	if strings.Join(actual, ",") != strings.Join(want, ",") {
		t.Errorf("%s fields = %v, want %v", definition.Name, actual, want)
	}
}

func namedType(value *ast.Type) string {
	if value == nil {
		return ""
	}
	if value.Elem != nil {
		return namedType(value.Elem)
	}
	return value.NamedType
}

func reverse[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
