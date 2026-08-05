package bind

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type testUser struct{}
type testPost struct{}
type testRole string

const (
	roleAdmin testRole = "admin"
	roleUser  testRole = "user"
)

type fixtureIDs struct {
	user, post                      golem.ModelID
	userID, userName, userRole      golem.FieldID
	userTags, userProfile           golem.FieldID
	userPosts, postID, postAuthorID golem.FieldID
	postAuthor                      golem.FieldID
	relation                        golem.RelationID
	enum                            ir.EnumID
	admin                           ir.EnumValueID
}

func TestPredicateBindsSchemaDirectedEnumAndRelationTree(t *testing.T) {
	registry, ids := bindFixture(t)
	name := golem.GeneratedNullableTextField[testUser, string](ids.userName)
	author := golem.GeneratedToOne[testPost, testUser](ids.postAuthor, ids.relation)
	descriptor := modelDescriptor[testPost](ids.post)

	frozen, err := author.Is(name.Eq("Ada")).Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := Predicate(frozen, registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	field, relation, target, cardinality, child, ok := condition.Relation()
	if !ok || field != ir.FieldID(ids.postAuthor) || relation != ir.RelationID(ids.relation) || target != ir.ModelID(ids.user) || cardinality != ir.RelationToOne || child == nil {
		t.Fatalf("relation = field=%x relation=%x target=%x cardinality=%d child=%v ok=%v", field, relation, target, cardinality, child, ok)
	}
	childField, ok := child.Field()
	if !ok || childField != ir.FieldID(ids.userName) || child.ModelID() != ir.ModelID(ids.user) {
		t.Fatalf("child field/model = %x/%x", childField, child.ModelID())
	}
	operatorID, _ := child.Operator()
	if operatorID != ir.OperatorEqual {
		t.Fatalf("child operator = %d", operatorID)
	}
	if err := operator.RequireAgreement(operatorID, ir.PortableProviders()); err != nil {
		t.Fatalf("binding construction did not retain the agreement-proved operator: %v", err)
	}

	role := golem.GeneratedEqualField[testUser, testRole](ids.userRole)
	frozenRole, err := role.Eq(roleAdmin).Freeze(modelDescriptor[testUser](ids.user))
	if err != nil {
		t.Fatal(err)
	}
	boundRole, err := Predicate(frozenRole, registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	operand, _ := boundRole.Operand()
	value, _ := operand.One()
	enum, member, ok := value.Enum()
	if !ok || enum != ids.enum || member != ids.admin {
		t.Fatalf("enum operand = %x/%x/%v", enum, member, ok)
	}
}

func TestPolicyPreservesRulePositionEffectAndFieldOrder(t *testing.T) {
	registry, ids := bindFixture(t)
	name := golem.GeneratedNullableTextField[testUser, string](ids.userName)
	posts := golem.GeneratedToMany[testUser, testPost](ids.userPosts, ids.relation)
	rules := golem.NewRules[testUser]()
	rules.CannotRead(golem.All[testUser]())
	rules.CanReadFields(name.Eq("Ada"), posts, name)
	frozen, err := rules.Freeze(ids.user)
	if err != nil {
		t.Fatal(err)
	}

	policy, err := Policy(frozen, registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	bound := policy.Rules()
	if len(bound) != 2 || bound[0].Position() != 0 || bound[1].Position() != 1 {
		t.Fatalf("rule positions = %#v", bound)
	}
	if bound[0].Effect() != ir.EffectDeny {
		t.Fatalf("first effect = %d", bound[0].Effect())
	}
	if _, present := bound[0].Condition(); present {
		t.Fatal("unconditional public rule did not remain unconditional")
	}
	fields, modelWide := bound[1].Fields()
	want := []ir.FieldID{ir.FieldID(ids.userPosts), ir.FieldID(ids.userName)}
	if modelWide || len(fields) != len(want) || fields[0] != want[0] || fields[1] != want[1] {
		t.Fatalf("field order/modelWide = %x/%v", fields, modelWide)
	}

	fields[0] = ir.FieldID{0xff}
	secondRead, _ := policy.Rules()[1].Fields()
	if secondRead[0] != want[0] {
		t.Fatal("bound policy field accessor leaked mutable storage")
	}
}

func TestListEqualityBindsAsOneTypedOrderedListValue(t *testing.T) {
	registry, ids := bindFixture(t)
	tags := golem.GeneratedListField[testUser, string](ids.userTags)
	frozen, err := tags.Eq(golem.List[string]{"backend", "go"}).Freeze(modelDescriptor[testUser](ids.user))
	if err != nil {
		t.Fatal(err)
	}
	condition, err := Predicate(frozen, registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	operatorID, _ := condition.Operator()
	operand, _ := condition.Operand()
	if operatorID != ir.OperatorListEqual || operand.Kind() != ir.OperandOne {
		t.Fatalf("operator/operand = %d/%d", operatorID, operand.Kind())
	}
	listValue, _ := operand.One()
	values, ok := listValue.List()
	if !ok || len(values) != 2 {
		t.Fatalf("list value = %#v/%v", values, ok)
	}
	first, _ := values[0].Text()
	second, _ := values[1].Text()
	if first != "backend" || second != "go" {
		t.Fatalf("list order = %q, %q", first, second)
	}
	typ, _ := condition.FieldType()
	if typ.Capability() != ir.CapabilityScalarListJSON {
		t.Fatalf("list capability = %d", typ.Capability())
	}
}

func TestActivatedTextAndListOperatorsBindToExactIRCells(t *testing.T) {
	registry, ids := bindFixture(t)
	descriptor := modelDescriptor[testUser](ids.user)
	name := golem.GeneratedNullableModeTextField[testUser, string](ids.userName)
	frozen, err := name.Compare(golem.ASCIIInsensitive()).StartsWith("Ad").Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := Predicate(frozen, registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	operatorID, _ := condition.Operator()
	mode, _ := condition.Mode()
	if operatorID != ir.OperatorStartsWith || mode != ir.ComparisonASCIIInsensitive {
		t.Fatalf("text operator/mode = %d/%d", operatorID, mode)
	}
	wantCapabilities := map[ir.Capability]bool{
		ir.CapabilityBinaryText:           false,
		ir.CapabilityASCIIInsensitiveText: false,
	}
	for _, requirement := range condition.Requirements() {
		if _, ok := wantCapabilities[requirement.Capability()]; ok {
			wantCapabilities[requirement.Capability()] = true
		}
	}
	for capability, found := range wantCapabilities {
		if !found {
			t.Fatalf("text condition lacks capability %d", capability)
		}
	}

	tags := golem.GeneratedNullableListField[testUser, string](ids.userTags)
	tests := []struct {
		name     string
		value    golem.Predicate[testUser]
		operator ir.OperatorID
		operand  ir.OperandKind
	}{
		{"has", tags.Has("go"), ir.OperatorListHas, ir.OperandOne},
		{"has every empty", tags.HasEvery(), ir.OperatorListHasEvery, ir.OperandMany},
		{"has some", tags.HasSome("go", "sql"), ir.OperatorListHasSome, ir.OperandMany},
		{"is empty", tags.IsEmpty(true), ir.OperatorListIsEmpty, ir.OperandFlag},
		{"is null", tags.IsNull(), ir.OperatorListIsNull, ir.OperandNone},
		{"is not null", tags.IsNotNull(), ir.OperatorListIsNotNull, ir.OperandNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, freezeErr := test.value.Freeze(descriptor)
			if freezeErr != nil {
				t.Fatal(freezeErr)
			}
			bound, bindErr := Predicate(frozen, registry, ir.PortableProviders())
			if bindErr != nil {
				t.Fatal(bindErr)
			}
			operatorID, _ := bound.Operator()
			operand, _ := bound.Operand()
			if operatorID != test.operator || operand.Kind() != test.operand {
				t.Fatalf("operator/operand = %d/%d; want %d/%d", operatorID, operand.Kind(), test.operator, test.operand)
			}
			if test.operand == ir.OperandMany {
				values, ok := operand.Many()
				if !ok || test.name == "has every empty" && len(values) != 0 {
					t.Fatalf("many operand = %#v/%v", values, ok)
				}
			}
		})
	}
}

func TestActivatedJSONOperatorsBindExactPathsValuesModesAndSentinels(t *testing.T) {
	registry, ids := bindFixture(t)
	descriptor := modelDescriptor[testUser](ids.user)
	profile := golem.GeneratedNullableModeJSONField[testUser](ids.userProfile)
	path := golem.NewJSONPath(golem.JSONKey("profile"), golem.JSONIndex(3))
	number, err := golem.ParseJSONNumber("9007199254740993.25")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		value     golem.Predicate[testUser]
		operator  ir.OperatorID
		mode      ir.ComparisonMode
		pathCount int
		operand   ir.OperandKind
		jsonKind  ir.JSONKind
		nullKind  ir.JSONNullKind
	}{
		{"root exact number", profile.Root().Eq(number), ir.OperatorJSONEqual, ir.ComparisonSensitive, 0, ir.OperandOne, ir.JSONNumber, 0},
		{"path db null", profile.At(path).Ne(golem.DBNull), ir.OperatorJSONNotEqual, ir.ComparisonSensitive, 2, ir.OperandJSONNull, 0, ir.JSONDbNull},
		{"path document null", profile.At(path).Eq(golem.JSONNull), ir.OperatorJSONEqual, ir.ComparisonSensitive, 2, ir.OperandJSONNull, 0, ir.JSONDocumentNull},
		{"path any null", profile.At(path).Eq(golem.AnyNull), ir.OperatorJSONEqual, ir.ComparisonSensitive, 2, ir.OperandJSONNull, 0, ir.JSONAnyNull},
		{"path order", profile.At(path).GTE(number), ir.OperatorJSONGreaterThanOrEqual, ir.ComparisonSensitive, 2, ir.OperandOne, ir.JSONNumber, 0},
		{"path insensitive string", profile.At(path).Compare(golem.ASCIIInsensitive()).Contains("Admin"), ir.OperatorJSONStringContains, ir.ComparisonASCIIInsensitive, 2, ir.OperandOne, ir.JSONString, 0},
		{"path array candidate", profile.At(path).ArrayStartsWith(golem.JSONObject(map[string]golem.JSONValue{"id": number})), ir.OperatorJSONArrayStartsWith, ir.ComparisonSensitive, 2, ir.OperandOne, ir.JSONObject, 0},
		{"column is null", profile.IsNull(), ir.OperatorJSONIsNull, ir.ComparisonSensitive, 0, ir.OperandNone, 0, 0},
		{"column is not null", profile.IsNotNull(), ir.OperatorJSONIsNotNull, ir.ComparisonSensitive, 0, ir.OperandNone, 0, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, freezeErr := test.value.Freeze(descriptor)
			if freezeErr != nil {
				t.Fatal(freezeErr)
			}
			condition, bindErr := Predicate(frozen, registry, ir.PortableProviders())
			if bindErr != nil {
				t.Fatal(bindErr)
			}
			operatorID, _ := condition.Operator()
			mode, _ := condition.Mode()
			boundPath, _ := condition.Path()
			operand, _ := condition.Operand()
			if operatorID != test.operator || mode != test.mode || len(boundPath.Segments()) != test.pathCount || operand.Kind() != test.operand {
				t.Fatalf("operator/mode/path/operand = %d/%d/%d/%d", operatorID, mode, len(boundPath.Segments()), operand.Kind())
			}
			if test.jsonKind != 0 {
				value, ok := operand.One()
				jsonValue, jsonOK := value.JSON()
				if !ok || !jsonOK || jsonValue.Kind() != test.jsonKind {
					t.Fatalf("JSON operand = %#v/%v/%v", jsonValue, ok, jsonOK)
				}
				if test.jsonKind == ir.JSONNumber {
					exact, _ := jsonValue.Number()
					if string(exact.Coefficient()) != "900719925474099325" || exact.Exponent() != -2 {
						t.Fatalf("exact number = %s e%d", exact.Coefficient(), exact.Exponent())
					}
				}
			}
			if test.nullKind != 0 {
				kind, ok := operand.JSONNull()
				if !ok || kind != test.nullKind {
					t.Fatalf("null operand = %d/%v", kind, ok)
				}
			}
		})
	}
}

func TestBinderFailsClosedOnStaleIdentitiesProvidersAndEnumLabels(t *testing.T) {
	registry, ids := bindFixture(t)
	descriptor := modelDescriptor[testPost](ids.post)

	tests := []struct {
		name   string
		freeze func() golem.FrozenPredicate
		code   ErrorCode
	}{
		{
			name: "cross-model field",
			freeze: func() golem.FrozenPredicate {
				value, err := golem.GeneratedTextField[testPost, string](ids.userName).Eq("Ada").Freeze(descriptor)
				if err != nil {
					t.Fatal(err)
				}
				return value
			},
			code: CodeField,
		},
		{
			name: "stale relation",
			freeze: func() golem.FrozenPredicate {
				value, err := golem.GeneratedToOne[testPost, testUser](ids.postAuthor, golem.RelationID{0xff}).Is(golem.All[testUser]()).Freeze(descriptor)
				if err != nil {
					t.Fatal(err)
				}
				return value
			},
			code: CodeRelation,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Predicate(test.freeze(), registry, ir.PortableProviders())
			var bindErr *Error
			if !errors.As(err, &bindErr) || bindErr.Code != test.code {
				t.Fatalf("error = %v, want code %s", err, test.code)
			}
		})
	}

	role := golem.GeneratedEqualField[testUser, testRole](ids.userRole)
	frozenRole, err := role.Eq(testRole("owner")).Freeze(modelDescriptor[testUser](ids.user))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Predicate(frozenRole, registry, ir.PortableProviders())
	var bindErr *Error
	if !errors.As(err, &bindErr) || bindErr.Code != CodeValue || bindErr.ModelID != ir.ModelID(ids.user) || bindErr.FieldID != ir.FieldID(ids.userRole) || bindErr.OperatorID != ir.OperatorEqual {
		t.Fatalf("unknown enum error = %v", err)
	}

	sqliteOnly, err := ir.NewProviderSet(ir.ProviderSQLite)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Predicate(frozenRole, registry, sqliteOnly)
	if !errors.As(err, &bindErr) || bindErr.Code != CodeProvider {
		t.Fatalf("narrowed provider error = %v", err)
	}

	rules := golem.NewRules[testUser]()
	rules.CanReadFields(golem.All[testUser](), golem.GeneratedTextField[testUser, string](golem.FieldID{0xff}))
	frozenPolicy, err := rules.Freeze(ids.user)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Policy(frozenPolicy, registry, ir.PortableProviders())
	if !errors.As(err, &bindErr) || bindErr.Code != CodeField || !bindErr.HasRule || bindErr.RulePosition != 0 || bindErr.FieldID != (ir.FieldID{0xff}) {
		t.Fatalf("typed rule diagnostic = %#v (%v)", bindErr, err)
	}
}

func TestPredicateBindingIsDeterministicAndConcurrent(t *testing.T) {
	registry, ids := bindFixture(t)
	name := golem.GeneratedNullableTextField[testUser, string](ids.userName)
	frozen, err := name.In("Ada", "Grace").Freeze(modelDescriptor[testUser](ids.user))
	if err != nil {
		t.Fatal(err)
	}
	want, err := Predicate(frozen, registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := ir.CanonicalCondition(want)
	if err != nil {
		t.Fatal(err)
	}

	errorsChannel := make(chan error, 16)
	for worker := 0; worker < cap(errorsChannel); worker++ {
		go func() {
			condition, bindErr := Predicate(frozen, registry, ir.PortableProviders())
			if bindErr != nil {
				errorsChannel <- bindErr
				return
			}
			canonical, canonicalErr := ir.CanonicalCondition(condition)
			if canonicalErr != nil {
				errorsChannel <- canonicalErr
				return
			}
			if string(canonical) != string(wantBytes) {
				errorsChannel <- fmt.Errorf("canonical condition changed")
				return
			}
			errorsChannel <- nil
		}()
	}
	for worker := 0; worker < cap(errorsChannel); worker++ {
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
	}
}

func bindFixture(t *testing.T) (*schema.Registry, fixtureIDs) {
	t.Helper()
	userID, postID := compilerir.ModelID(bindID(1)), compilerir.ModelID(bindID(2))
	userKey, userName, userRole, userTags, userProfile, userPosts := compilerir.FieldID(bindID(11)), compilerir.FieldID(bindID(12)), compilerir.FieldID(bindID(13)), compilerir.FieldID(bindID(14)), compilerir.FieldID(bindID(15)), compilerir.FieldID(bindID(16))
	postKey, postAuthorID, postAuthor := compilerir.FieldID(bindID(21)), compilerir.FieldID(bindID(22)), compilerir.FieldID(bindID(23))
	relationID := compilerir.RelationID(bindID(31))
	enumID, adminID, ordinaryID := compilerir.EnumID(bindID(51)), compilerir.EnumValueID(bindID(52)), compilerir.EnumValueID(bindID(53))
	maxLength := uint32(128)
	listCapability := compilerir.CapabilityID("scalar-list:json-array:v1")
	listElement := compilerir.LogicalTypeIR{Kind: compilerir.TypeString}
	model := compilerir.ModelIR{
		FormatVersion: compilerir.ModelFormatVersion,
		Providers:     []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL},
		Enums: []compilerir.EnumIR{{ID: enumID, LogicalName: "Role", Values: []compilerir.EnumValueIR{
			{ID: adminID, GoName: "RoleAdmin", WireValue: string(roleAdmin)},
			{ID: ordinaryID, GoName: "RoleUser", WireValue: string(roleUser)},
		}}},
		Models: []compilerir.ModelDeclIR{
			{ID: userID, LogicalName: "User", Table: compilerir.TableBindingIR{PhysicalName: "users"}, Fields: []compilerir.FieldIR{
				bindScalar(userKey, "ID", "id", compilerir.FieldScalar, compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}, false, 0),
				bindScalar(userName, "Name", "name", compilerir.FieldScalar, compilerir.LogicalTypeIR{Kind: compilerir.TypeString, MaxLength: &maxLength}, true, 1),
				bindScalar(userRole, "Role", "role", compilerir.FieldEnum, compilerir.LogicalTypeIR{Kind: compilerir.TypeEnum, EnumID: &enumID}, false, 2),
				bindScalar(userTags, "Tags", "tags", compilerir.FieldScalarList, compilerir.LogicalTypeIR{Kind: compilerir.TypeScalarList, Element: &listElement, Capability: &listCapability}, true, 3),
				bindScalar(userProfile, "Profile", "profile", compilerir.FieldScalar, compilerir.LogicalTypeIR{Kind: compilerir.TypeJSON}, true, 4),
				bindRelation(userPosts, "Posts", relationID, compilerir.RelationInverse, compilerir.RelationHasMany, 5),
			}, PrimaryKey: &compilerir.KeyIR{ID: compilerir.KeyID(bindID(41)), Kind: compilerir.KeyPrimary, PhysicalName: "pk_users", Fields: []compilerir.FieldID{userKey}}},
			{ID: postID, LogicalName: "Post", Table: compilerir.TableBindingIR{PhysicalName: "posts"}, Fields: []compilerir.FieldIR{
				bindScalar(postKey, "ID", "id", compilerir.FieldScalar, compilerir.LogicalTypeIR{Kind: compilerir.TypeString, MaxLength: &maxLength}, false, 0),
				bindScalar(postAuthorID, "AuthorID", "author_id", compilerir.FieldScalar, compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}, false, 1),
				bindRelation(postAuthor, "Author", relationID, compilerir.RelationSource, compilerir.RelationBelongsTo, 2),
			}, PrimaryKey: &compilerir.KeyIR{ID: compilerir.KeyID(bindID(42)), Kind: compilerir.KeyPrimary, PhysicalName: "pk_posts", Fields: []compilerir.FieldID{postKey}}},
		},
		Relations: []compilerir.RelationIR{{ID: relationID, SourceModel: postID, TargetModel: userID, SourceField: postAuthor, InverseField: &userPosts, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{postAuthorID}, RemoteFields: []compilerir.FieldID{userKey}}},
	}
	contract := compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, Models: []compilerir.ModelContractIR{
		{ModelID: userID, Fields: bindContractFields(userKey, userName, userRole, userTags, userProfile, userPosts)},
		{ModelID: postID, Fields: bindContractFields(postKey, postAuthorID, postAuthor)},
	}, Enums: []compilerir.EnumContractIR{{EnumID: enumID, GraphQLName: "Role"}}}

	modelBytes, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	modelDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), bindDigest(t, string(modelFingerprint)), modelBytes)
	contractDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), bindDigest(t, string(contractFingerprint)), contractBytes)

	sqliteSchema, err := sqlite.New().Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	postgresSchema, err := postgresql.New().Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bundle := golem.GeneratedSchemaBundle(golem.SchemaDigest{1}, "generator", "abi", modelDocument, contractDocument,
		bindProviderDocument(t, golem.SQLite, sqliteSchema), bindProviderDocument(t, golem.PostgreSQL, postgresSchema))
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return registry, fixtureIDs{
		user: bindModelID(t, userID), post: bindModelID(t, postID), userID: bindFieldID(t, userKey), userName: bindFieldID(t, userName), userRole: bindFieldID(t, userRole), userTags: bindFieldID(t, userTags), userProfile: bindFieldID(t, userProfile),
		userPosts: bindFieldID(t, userPosts), postID: bindFieldID(t, postKey), postAuthorID: bindFieldID(t, postAuthorID), postAuthor: bindFieldID(t, postAuthor),
		relation: bindRelationID(t, relationID), enum: ir.EnumID(bindFixedID(t, string(enumID))), admin: ir.EnumValueID(bindFixedID(t, string(adminID))),
	}
}

func bindScalar(id compilerir.FieldID, name, column string, kind compilerir.FieldKind, typ compilerir.LogicalTypeIR, nullable bool, order uint32) compilerir.FieldIR {
	return compilerir.FieldIR{ID: id, GoName: name, LogicalName: name, DeclarationOrder: order, Kind: kind, Scalar: &compilerir.ScalarFieldIR{Column: compilerir.SQLIdentifier(column), Type: typ, Nullable: nullable}}
}

func bindRelation(id compilerir.FieldID, name string, relation compilerir.RelationID, role compilerir.RelationEndpointRole, kind compilerir.RelationKind, order uint32) compilerir.FieldIR {
	return compilerir.FieldIR{ID: id, GoName: name, LogicalName: name, DeclarationOrder: order, Kind: compilerir.FieldRelation, Relation: &compilerir.RelationFieldIR{RelationID: relation, Role: role, Kind: kind}}
}

func bindContractFields(ids ...compilerir.FieldID) []compilerir.FieldContractIR {
	result := make([]compilerir.FieldContractIR, len(ids))
	for index, id := range ids {
		result[index] = compilerir.FieldContractIR{FieldID: id}
	}
	return result
}

func bindProviderDocument(t *testing.T, provider golem.Provider, value physical.PhysicalSchema) golem.ProviderSchemaDocument {
	t.Helper()
	payload, err := physical.CanonicalEncode(value)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := physical.PhysicalFingerprint(value)
	if err != nil {
		t.Fatal(err)
	}
	system, err := physical.SystemFingerprint(value.Provider, value.System)
	if err != nil {
		t.Fatal(err)
	}
	document := golem.GeneratedSchemaDocument(value.Version, value.CanonicalVersion, golem.SchemaDigest(fingerprint), payload)
	return golem.GeneratedProviderSchemaDocument(provider, golem.SchemaDigest(system), document)
}

func modelDescriptor[M any](model golem.ModelID) golem.ModelDescriptor[M] {
	return golem.GeneratedModelDescriptor[M](model, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
}

func bindDigest(t *testing.T, value string) golem.SchemaDigest {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("digest %q: %v", value, err)
	}
	var result golem.SchemaDigest
	copy(result[:], decoded)
	return result
}

func bindFixedID(t *testing.T, value string) [16]byte {
	t.Helper()
	parsed, err := fixedID(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func bindModelID(t *testing.T, value compilerir.ModelID) golem.ModelID {
	return golem.ModelID(bindFixedID(t, string(value)))
}

func bindFieldID(t *testing.T, value compilerir.FieldID) golem.FieldID {
	return golem.FieldID(bindFixedID(t, string(value)))
}

func bindRelationID(t *testing.T, value compilerir.RelationID) golem.RelationID {
	return golem.RelationID(bindFixedID(t, string(value)))
}

func bindID(value int) string { return fmt.Sprintf("%032x", value) }
