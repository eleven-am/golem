package sql

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

func TestCompileOwnsDeterministicTraversalAliasesAndArguments(t *testing.T) {
	ids := testIDs{}
	providers := ir.PortableProviders()
	stringType, _ := ir.NewTypeRef(ir.ValueString, true, 0, 0, ir.EnumID{}, nil, 0)
	operandValue, _ := ir.StringValue("A%_\\z")
	operand, _ := ir.OneOperand(operandValue)
	requirements, err := operator.ValidateShape(ir.OperatorContains, operator.Shape{Node: ir.ConditionScalar, FieldType: stringType, Operand: operand, Mode: ir.ComparisonSensitive, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	child, _ := ir.NewScalar(ids.childModel(), ids.childName(), stringType, ir.OperatorContains, ir.ComparisonSensitive, operand, requirements)
	relationRequirements, err := operator.ValidateShape(ir.OperatorRelationEvery, operator.Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: ir.RelationToMany, HasChild: true, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	relation, _ := ir.NewRelation(ids.rootModel(), ids.children(), ids.relation(), ids.childModel(), ir.RelationToMany, ir.OperatorRelationEvery, &child, relationRequirements)
	localValue, _ := ir.StringValue("root")
	localOperand, _ := ir.OneOperand(localValue)
	localRequirements, _ := operator.ValidateShape(ir.OperatorEqual, operator.Shape{Node: ir.ConditionScalar, FieldType: stringType, Operand: localOperand, Mode: ir.ComparisonSensitive, Providers: providers})
	local, _ := ir.NewScalar(ids.rootModel(), ids.rootName(), stringType, ir.OperatorEqual, ir.ComparisonSensitive, localOperand, localRequirements)
	condition, _ := ir.NewLogical(ids.rootModel(), ir.LogicalAnd, []ir.Condition{local, relation})

	resolver := fixtureResolver(ids, stringType)
	request := testRequest(t, condition, ir.ProviderPostgreSQL, resolver, "root_alias")
	first, err := Compile(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(request)
	if err != nil {
		t.Fatal(err)
	}
	want := `(("root_alias"."name" = $1)) AND (NOT (EXISTS (SELECT 1 FROM "app"."children" AS "golem_p1" WHERE "root_alias"."tenant" = "golem_p1"."tenant" AND "root_alias"."id" = "golem_p1"."parent_id" AND ((("golem_p1"."name" LIKE $2)) IS NOT TRUE))))`
	if first.SQL() != want {
		t.Fatalf("SQL:\n%s\nwant:\n%s", first.SQL(), want)
	}
	if second.SQL() != first.SQL() || !reflect.DeepEqual(first.Args(), []any{"root", "A%_\\z"}) || !reflect.DeepEqual(second.Args(), first.Args()) {
		t.Fatalf("non-deterministic fragment: %#v %#v", first.Args(), second.Args())
	}
}

func TestCompileFailsClosedBeforeDialectOnCapabilityOrDescriptorDrift(t *testing.T) {
	ids := testIDs{}
	typ, _ := ir.NewTypeRef(ir.ValueString, false, 0, 0, ir.EnumID{}, nil, 0)
	value, _ := ir.StringValue("x")
	operand, _ := ir.OneOperand(value)
	requirements, _ := operator.ValidateShape(ir.OperatorEqual, operator.Shape{Node: ir.ConditionScalar, FieldType: typ, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
	condition, _ := ir.NewScalar(ids.rootModel(), ids.rootName(), typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, requirements)
	resolver := fixtureResolver(ids, typ)
	delete(resolver.capabilities, ir.CapabilityBinaryText)
	_, err := Compile(testRequest(t, condition, ir.ProviderSQLite, resolver, "root"))
	assertCode(t, err, CodeCapability)
	resolver = fixtureResolver(ids, typ)
	delete(resolver.fields, fieldKey{model: ids.rootModel(), field: ids.rootName()})
	_, err = Compile(testRequest(t, condition, ir.ProviderSQLite, resolver, "root"))
	assertCode(t, err, CodeSchema)
	resolver = fixtureResolver(ids, typ)
	wrongType, _ := ir.NewTypeRef(ir.ValueInt64, false, 0, 0, ir.EnumID{}, nil, 0)
	field := resolver.fields[fieldKey{model: ids.rootModel(), field: ids.rootName()}]
	field.Type = wrongType
	resolver.fields[fieldKey{model: ids.rootModel(), field: ids.rootName()}] = field
	_, err = Compile(testRequest(t, condition, ir.ProviderSQLite, resolver, "root"))
	assertCode(t, err, CodeSchema)
}

func TestCompileRejectsAliasCaptureAndMismatchedRuntimeProof(t *testing.T) {
	ids := testIDs{}
	condition, _ := ir.NewConstant(ids.rootModel(), true)
	resolver := fixtureResolver(ids, mustType(t, ir.ValueString))
	_, err := Compile(testRequest(t, condition, ir.ProviderSQLite, resolver, "golem_p1"))
	assertCode(t, err, CodeInput)
	request := testRequest(t, condition, ir.ProviderSQLite, resolver, "root")
	request.BoundFingerprint = [32]byte{2}
	_, err = Compile(request)
	assertCode(t, err, CodeSchema)
	request = testRequest(t, condition, ir.ProviderSQLite, resolver, "root")
	request.Capabilities, _ = NewCapabilityProof(ir.ProviderPostgreSQL, resolver.SchemaFingerprint())
	_, err = Compile(request)
	assertCode(t, err, CodeCapability)
}

func mustType(t *testing.T, kind ir.ValueKind) ir.TypeRef {
	t.Helper()
	value, err := ir.NewTypeRef(kind, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestFragmentArgumentsAreCopied(t *testing.T) {
	fragment := Fragment{text: "x", args: []any{[]byte{1, 2}}}
	first := fragment.Args()
	first[0].([]byte)[0] = 9
	if got := fragment.Args()[0].([]byte)[0]; got != 1 {
		t.Fatalf("argument alias leaked: %d", got)
	}
}

func assertCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	typed, ok := err.(*Error)
	if !ok || typed.Code != code {
		t.Fatalf("error = %T %v, want %s", err, err, code)
	}
}

type testIDs struct{}

func (testIDs) rootModel() ir.ModelID   { return ir.ModelID{1} }
func (testIDs) childModel() ir.ModelID  { return ir.ModelID{2} }
func (testIDs) rootName() ir.FieldID    { return ir.FieldID{3} }
func (testIDs) childName() ir.FieldID   { return ir.FieldID{4} }
func (testIDs) children() ir.FieldID    { return ir.FieldID{5} }
func (testIDs) relation() ir.RelationID { return ir.RelationID{6} }
func (testIDs) rootTenant() ir.FieldID  { return ir.FieldID{7} }
func (testIDs) rootID() ir.FieldID      { return ir.FieldID{8} }
func (testIDs) childTenant() ir.FieldID { return ir.FieldID{9} }
func (testIDs) childParent() ir.FieldID { return ir.FieldID{10} }

type fieldKey struct {
	model ir.ModelID
	field ir.FieldID
}
type fakeResolver struct {
	models       map[ir.ModelID]Model
	fields       map[fieldKey]Field
	relations    map[ir.RelationID]Relation
	capabilities map[ir.Capability]bool
}

func testRequest(t *testing.T, condition ir.Condition, provider ir.Provider, resolver *fakeResolver, alias physical.PhysicalName) Request {
	t.Helper()
	proof, err := NewCapabilityProof(provider, resolver.SchemaFingerprint(), ir.CapabilityBinaryText, ir.CapabilityASCIIInsensitiveText, ir.CapabilityExactJSON, ir.CapabilityScalarListJSON, ir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	return Request{Condition: condition, Provider: provider, Resolver: resolver, Dialect: testDialect{provider: provider}, Capabilities: proof, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: alias}
}

func fixtureResolver(ids testIDs, stringType ir.TypeRef) *fakeResolver {
	intType, _ := ir.NewTypeRef(ir.ValueInt64, false, 0, 0, ir.EnumID{}, nil, 0)
	fields := map[fieldKey]Field{}
	add := func(model ir.ModelID, id ir.FieldID, name string, typ ir.TypeRef) {
		fields[fieldKey{model, id}] = Field{Model: model, ID: id, Column: physical.PhysicalName(name), Type: typ, Nullable: typ.Nullable()}
	}
	add(ids.rootModel(), ids.rootName(), "name", stringType)
	add(ids.childModel(), ids.childName(), "name", stringType)
	add(ids.rootModel(), ids.rootTenant(), "tenant", intType)
	add(ids.rootModel(), ids.rootID(), "id", intType)
	add(ids.childModel(), ids.childTenant(), "tenant", intType)
	add(ids.childModel(), ids.childParent(), "parent_id", intType)
	return &fakeResolver{
		models:       map[ir.ModelID]Model{ids.rootModel(): {ID: ids.rootModel(), Namespace: "app", Table: "roots"}, ids.childModel(): {ID: ids.childModel(), Namespace: "app", Table: "children"}},
		fields:       fields,
		relations:    map[ir.RelationID]Relation{ids.relation(): {Model: ids.rootModel(), Field: ids.children(), ID: ids.relation(), Target: ids.childModel(), Cardinality: ir.RelationToMany, Pairs: []Correlation{{Parent: ids.rootTenant(), Child: ids.childTenant()}, {Parent: ids.rootID(), Child: ids.childParent()}}}},
		capabilities: map[ir.Capability]bool{ir.CapabilityBinaryText: true, ir.CapabilityASCIIInsensitiveText: true, ir.CapabilityExactJSON: true, ir.CapabilityScalarListJSON: true, ir.CapabilityRelationCorrelation: true},
	}
}
func (*fakeResolver) Providers() ir.ProviderSet   { return ir.PortableProviders() }
func (*fakeResolver) SchemaFingerprint() [32]byte { return [32]byte{1} }
func (resolver *fakeResolver) Model(_ ir.Provider, model ir.ModelID) (Model, bool) {
	value, ok := resolver.models[model]
	return value, ok
}
func (resolver *fakeResolver) Field(_ ir.Provider, model ir.ModelID, field ir.FieldID) (Field, bool) {
	value, ok := resolver.fields[fieldKey{model, field}]
	return value, ok
}
func (resolver *fakeResolver) Relation(model ir.ModelID, field ir.FieldID, relation ir.RelationID) (Relation, bool) {
	value, ok := resolver.relations[relation]
	return value, ok && value.Model == model && value.Field == field
}
func (*fakeResolver) EnumWire(ir.EnumID, ir.EnumValueID) (string, bool) { return "enum", true }
func (resolver *fakeResolver) Capability(_ ir.Provider, capability ir.Capability) bool {
	return resolver.capabilities[capability]
}

type testDialect struct{ provider ir.Provider }

func (dialect testDialect) Provider() ir.Provider           { return dialect.provider }
func (testDialect) Quote(name physical.PhysicalName) string { return `"` + string(name) + `"` }
func (dialect testDialect) Table(model Model) string {
	if dialect.provider == ir.ProviderSQLite {
		return dialect.Quote(model.Table)
	}
	return dialect.Quote(model.Namespace) + "." + dialect.Quote(model.Table)
}
func (dialect testDialect) Placeholder(position int) string {
	if dialect.provider == ir.ProviderSQLite {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}
func (testDialect) Supports(ir.OperatorID) bool { return true }
func (testDialect) Encode(bound BoundValue) (any, error) {
	if text, ok := bound.Value.Text(); ok {
		return text, nil
	}
	return nil, fmt.Errorf("test codec")
}
func (dialect testDialect) RenderScalar(leaf ScalarLeaf, binder *Binder) (string, error) {
	value, _ := leaf.Operand.One()
	placeholder, err := binder.Value(value, leaf.Field.Type)
	if err != nil {
		return "", err
	}
	op := " = "
	if leaf.Operator == ir.OperatorContains {
		op = " LIKE "
	}
	return dialect.Quote(leaf.Column.Alias) + "." + dialect.Quote(leaf.Column.Name) + op + placeholder, nil
}
func (testDialect) RenderList(ListLeaf, *Binder) (string, error) {
	return "", fmt.Errorf("unsupported")
}
func (testDialect) RenderJSON(JSONLeaf, *Binder) (string, error) {
	return "", fmt.Errorf("unsupported")
}
