package sql

import (
	"fmt"
	"reflect"
	"strings"
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

func TestCompileNamedRelationAndCombinatorMutations(t *testing.T) {
	ids := testIDs{}
	providers := ir.PortableProviders()
	stringType, _ := ir.NewTypeRef(ir.ValueString, true, 0, 0, ir.EnumID{}, nil, 0)
	value, _ := ir.StringValue("Ada")
	operand, _ := ir.OneOperand(value)
	leafRequirements, err := operator.ValidateShape(ir.OperatorEqual, operator.Shape{Node: ir.ConditionScalar, FieldType: stringType, Operand: operand, Mode: ir.ComparisonSensitive, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := ir.NewScalar(ids.childModel(), ids.childName(), stringType, ir.OperatorEqual, ir.ComparisonSensitive, operand, leafRequirements)
	relationCondition := func(operatorID ir.OperatorID, cardinality ir.RelationCardinality, child *ir.Condition) ir.Condition {
		requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: cardinality, HasChild: child != nil, Providers: providers})
		if err != nil {
			t.Fatal(err)
		}
		condition, err := ir.NewRelation(ids.rootModel(), ids.children(), ids.relation(), ids.childModel(), cardinality, operatorID, child, requirements)
		if err != nil {
			t.Fatal(err)
		}
		return condition
	}

	t.Run("M1_every_nullable_child_uses_is_not_true", func(t *testing.T) {
		fragment, err := Compile(testRequest(t, relationCondition(ir.OperatorRelationEvery, ir.RelationToMany, &leaf), ir.ProviderPostgreSQL, fixtureResolver(ids, stringType), "root"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(fragment.SQL(), `AND ((("golem_p1"."name" = $1)) IS NOT TRUE)`) || !strings.HasPrefix(fragment.SQL(), "NOT (EXISTS ") {
			t.Fatalf("every lost its two-valued counterexample form: %s", fragment.SQL())
		}
	})

	t.Run("M17_to_one_presence_uses_related_existence", func(t *testing.T) {
		resolver := fixtureResolver(ids, stringType)
		descriptor := resolver.relations[ids.relation()]
		descriptor.Cardinality = ir.RelationToOne
		resolver.relations[ids.relation()] = descriptor
		fragment, err := Compile(testRequest(t, relationCondition(ir.OperatorRelationIsNull, ir.RelationToOne, nil), ir.ProviderPostgreSQL, resolver, "root"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(fragment.SQL(), "NOT (EXISTS ") || strings.Contains(fragment.SQL(), " IS NULL") || !strings.Contains(fragment.SQL(), `"root"."tenant" = "golem_p1"."tenant"`) || !strings.Contains(fragment.SQL(), `"root"."id" = "golem_p1"."parent_id"`) {
			t.Fatalf("relation presence is not descriptor-correlated existence: %s", fragment.SQL())
		}
	})

	t.Run("M18_to_many_quantifier_matrix", func(t *testing.T) {
		want := map[ir.OperatorID]struct {
			prefix    string
			isNotTrue bool
		}{
			ir.OperatorRelationSome:  {prefix: "EXISTS ", isNotTrue: false},
			ir.OperatorRelationEvery: {prefix: "NOT (EXISTS ", isNotTrue: true},
			ir.OperatorRelationNone:  {prefix: "NOT (EXISTS ", isNotTrue: false},
		}
		for operatorID, expected := range want {
			fragment, err := Compile(testRequest(t, relationCondition(operatorID, ir.RelationToMany, &leaf), ir.ProviderPostgreSQL, fixtureResolver(ids, stringType), "root"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(fragment.SQL(), expected.prefix) || strings.Contains(fragment.SQL(), "IS NOT TRUE") != expected.isNotTrue {
				t.Fatalf("operator %d rendered %q", operatorID, fragment.SQL())
			}
		}
	})

	t.Run("M19_not_array_is_nor", func(t *testing.T) {
		truth, _ := ir.NewConstant(ids.rootModel(), true)
		falsehood, _ := ir.NewConstant(ids.rootModel(), false)
		or, _ := ir.NewLogical(ids.rootModel(), ir.LogicalOr, []ir.Condition{truth, falsehood})
		nor, _ := ir.NewLogical(ids.rootModel(), ir.LogicalNot, []ir.Condition{or})
		fragment, err := Compile(testRequest(t, nor, ir.ProviderPostgreSQL, fixtureResolver(ids, stringType), "root"))
		if err != nil {
			t.Fatal(err)
		}
		if fragment.SQL() != "NOT (((TRUE) OR (FALSE)))" {
			t.Fatalf("NOT(array) did not retain NOT(OR(...)) ownership: %s", fragment.SQL())
		}
	})

	t.Run("M20_empty_combinator_constants", func(t *testing.T) {
		for truth, want := range map[bool]string{true: "TRUE", false: "FALSE"} {
			condition, _ := ir.NewConstant(ids.rootModel(), truth)
			fragment, err := Compile(testRequest(t, condition, ir.ProviderPostgreSQL, fixtureResolver(ids, stringType), "root"))
			if err != nil {
				t.Fatal(err)
			}
			if fragment.SQL() != want {
				t.Fatalf("constant %v rendered %q", truth, fragment.SQL())
			}
		}
	})
}

func TestCompileNamedCapabilityMutationsRefuseBeforeDialectRendering(t *testing.T) {
	ids := testIDs{}
	providers := ir.PortableProviders()
	assertRefusal := func(t *testing.T, condition ir.Condition, typ ir.TypeRef, proved ...ir.Capability) {
		t.Helper()
		resolver := fixtureResolver(ids, typ)
		proof, err := NewCapabilityProof(ir.ProviderPostgreSQL, resolver.SchemaFingerprint(), proved...)
		if err != nil {
			t.Fatal(err)
		}
		request := Request{Condition: condition, Provider: ir.ProviderPostgreSQL, Resolver: resolver, Dialect: noRenderDialect{}, Capabilities: proof, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: "root"}
		_, err = Compile(request)
		assertCode(t, err, CodeCapability)
	}

	t.Run("M29_ascii_insensitive_missing_capability_refuses_before_sql", func(t *testing.T) {
		textType, _ := ir.NewTypeRef(ir.ValueString, true, 0, 0, ir.EnumID{}, nil, 0)
		textValue, _ := ir.StringValue("")
		one, _ := ir.OneOperand(textValue)
		emptyMany, _ := ir.ManyOperand(nil)
		for _, operatorID := range []ir.OperatorID{
			ir.OperatorEqual, ir.OperatorNotEqual, ir.OperatorIn, ir.OperatorNotIn,
			ir.OperatorLessThan, ir.OperatorLessThanOrEqual, ir.OperatorGreaterThan, ir.OperatorGreaterThanOrEqual,
			ir.OperatorContains, ir.OperatorStartsWith, ir.OperatorEndsWith,
		} {
			operatorID := operatorID
			t.Run(fmt.Sprintf("scalar_%d", operatorID), func(t *testing.T) {
				operand := one
				if operatorID == ir.OperatorIn || operatorID == ir.OperatorNotIn {
					operand = emptyMany // Degenerate forms must not bypass the gate.
				}
				requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionScalar, FieldType: textType, Operand: operand, Mode: ir.ComparisonASCIIInsensitive, Providers: providers})
				if err != nil {
					t.Fatal(err)
				}
				condition, err := ir.NewScalar(ids.rootModel(), ids.rootName(), textType, operatorID, ir.ComparisonASCIIInsensitive, operand, requirements)
				if err != nil {
					t.Fatal(err)
				}
				assertRefusal(t, condition, textType, ir.CapabilityBinaryText)
			})
		}

		jsonType, _ := ir.NewTypeRef(ir.ValueJSON, true, 0, 0, ir.EnumID{}, nil, 0)
		jsonString, _ := ir.JSONStringValue("")
		jsonValue, _ := ir.NewJSONValue(jsonString)
		jsonOperand, _ := ir.OneOperand(jsonValue)
		path, _ := ir.NewJSONPath()
		for _, operatorID := range []ir.OperatorID{ir.OperatorJSONStringContains, ir.OperatorJSONStringStartsWith, ir.OperatorJSONStringEndsWith} {
			operatorID := operatorID
			t.Run(fmt.Sprintf("json_%d", operatorID), func(t *testing.T) {
				requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionJSON, FieldType: jsonType, Operand: jsonOperand, Mode: ir.ComparisonASCIIInsensitive, Path: path, Providers: providers})
				if err != nil {
					t.Fatal(err)
				}
				condition, err := ir.NewJSON(ids.rootModel(), ids.rootName(), jsonType, operatorID, ir.ComparisonASCIIInsensitive, path, jsonOperand, requirements)
				if err != nil {
					t.Fatal(err)
				}
				assertRefusal(t, condition, jsonType, ir.CapabilityBinaryText, ir.CapabilityExactJSON)
			})
		}
	})

	t.Run("M30_scalar_list_json_capability_missing_refuses_before_sql", func(t *testing.T) {
		elementType, _ := ir.NewTypeRef(ir.ValueString, false, 0, 0, ir.EnumID{}, nil, 0)
		listType, _ := ir.NewTypeRef(ir.ValueScalarList, true, 0, 0, ir.EnumID{}, &elementType, ir.CapabilityScalarListJSON)
		listElement, _ := ir.StringValue("x")
		oneElement, _ := ir.OneOperand(listElement)
		listValue, _ := ir.NewListValue([]ir.Value{listElement})
		oneList, _ := ir.OneOperand(listValue)
		manyElements, _ := ir.ManyOperand([]ir.Value{listElement})
		for _, test := range []struct {
			operator ir.OperatorID
			operand  ir.Operand
		}{
			{ir.OperatorListEqual, oneList}, {ir.OperatorListHas, oneElement},
			{ir.OperatorListHasEvery, manyElements}, {ir.OperatorListHasSome, manyElements},
			{ir.OperatorListIsEmpty, ir.FlagOperand(true)},
			{ir.OperatorListIsNull, ir.NoOperand()}, {ir.OperatorListIsNotNull, ir.NoOperand()},
		} {
			test := test
			t.Run(fmt.Sprintf("list_%d", test.operator), func(t *testing.T) {
				requirements, err := operator.ValidateShape(test.operator, operator.Shape{Node: ir.ConditionList, FieldType: listType, Operand: test.operand, Mode: ir.ComparisonSensitive, Providers: providers})
				if err != nil {
					t.Fatal(err)
				}
				condition, err := ir.NewList(ids.rootModel(), ids.rootName(), listType, test.operator, test.operand, requirements)
				if err != nil {
					t.Fatal(err)
				}
				assertRefusal(t, condition, listType, ir.CapabilityBinaryText, ir.CapabilityASCIIInsensitiveText, ir.CapabilityExactJSON)
			})
		}
	})
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

// noRenderDialect panics on every rendering operation. Tests using it prove
// capability refusal happens before identifier quoting, operator support,
// binding, or renderer dispatch.
type noRenderDialect struct{}

func (noRenderDialect) Provider() ir.Provider { return ir.ProviderPostgreSQL }
func (noRenderDialect) Quote(physical.PhysicalName) string {
	panic("dialect rendering reached")
}
func (noRenderDialect) Table(Model) string { panic("dialect rendering reached") }
func (noRenderDialect) Placeholder(int) string {
	panic("dialect rendering reached")
}
func (noRenderDialect) Supports(ir.OperatorID) bool { panic("dialect rendering reached") }
func (noRenderDialect) Encode(BoundValue) (any, error) {
	panic("dialect rendering reached")
}
func (noRenderDialect) RenderScalar(ScalarLeaf, *Binder) (string, error) {
	panic("dialect rendering reached")
}
func (noRenderDialect) RenderList(ListLeaf, *Binder) (string, error) {
	panic("dialect rendering reached")
}
func (noRenderDialect) RenderJSON(JSONLeaf, *Binder) (string, error) {
	panic("dialect rendering reached")
}
