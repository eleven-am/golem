package sql

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

func TestCompilePolicyAliasFactsRootConditionHasNoOwnedAlias(t *testing.T) {
	ids := testIDs{}
	condition, err := ir.NewConstant(ids.rootModel(), true)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := Compile(testRequest(t, condition, ir.ProviderPostgreSQL, fixtureResolver(ids, mustType(t, ir.ValueString)), "root"))
	if err != nil {
		t.Fatal(err)
	}
	if facts := fragment.PolicyRelationAliases(); len(facts) != 0 {
		t.Fatalf("root-only fragment claimed policy relation aliases: %#v", facts)
	}
}

func TestCompilePolicyAliasFactsSingleAndNestedRelationsAreExactDeterministicAndImmutable(t *testing.T) {
	ids := testIDs{}
	grandModel := ir.ModelID{11}
	childGrandchildren := ir.FieldID{12}
	grandRelation := ir.RelationID{13}
	childID := ir.FieldID{14}
	grandParent := ir.FieldID{15}

	resolver := fixtureResolver(ids, mustType(t, ir.ValueString))
	resolver.models[grandModel] = Model{ID: grandModel, Namespace: "app", Table: "grandchildren"}
	intType := mustType(t, ir.ValueInt64)
	resolver.fields[fieldKey{model: ids.childModel(), field: childID}] = Field{Model: ids.childModel(), ID: childID, Column: "id", Type: intType}
	resolver.fields[fieldKey{model: grandModel, field: grandParent}] = Field{Model: grandModel, ID: grandParent, Column: "child_id", Type: intType}
	resolver.relations[grandRelation] = Relation{
		Model: ids.childModel(), Field: childGrandchildren, ID: grandRelation,
		Target: grandModel, Cardinality: ir.RelationToMany,
		Pairs: []Correlation{{Parent: childID, Child: grandParent}},
	}

	grandCondition, err := ir.NewConstant(grandModel, true)
	if err != nil {
		t.Fatal(err)
	}
	nested := relationCondition(t, ids.childModel(), childGrandchildren, grandRelation, grandModel, ir.RelationToMany, &grandCondition)
	root := relationCondition(t, ids.rootModel(), ids.children(), ids.relation(), ids.childModel(), ir.RelationToMany, &nested)

	first, err := Compile(testRequest(t, root, ir.ProviderPostgreSQL, resolver, "root"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(testRequest(t, root, ir.ProviderPostgreSQL, resolver, "root"))
	if err != nil {
		t.Fatal(err)
	}
	if first.SQL() != second.SQL() || !reflect.DeepEqual(first.Args(), second.Args()) {
		t.Fatal("alias fact registration changed deterministic SQL or bind output")
	}

	facts := first.PolicyRelationAliases()
	if len(facts) != 2 {
		t.Fatalf("policy relation alias count = %d, want 2", len(facts))
	}
	assertPolicyAliasFact(t, facts[0], "golem_p1", ids.childModel(), ids.relation())
	assertPolicyAliasFact(t, facts[1], "golem_p2", grandModel, grandRelation)
	if !reflect.DeepEqual(facts, second.PolicyRelationAliases()) {
		t.Fatalf("policy relation alias facts are non-deterministic:\n%#v\n%#v", facts, second.PolicyRelationAliases())
	}

	// The returned slice is caller-owned. Replacing a fact cannot alter the
	// fragment's privately retained registry.
	facts[0] = PolicyRelationAliasFact{}
	fresh := first.PolicyRelationAliases()
	if len(fresh) != 2 {
		t.Fatalf("caller mutation changed fact count: %d", len(fresh))
	}
	assertPolicyAliasFact(t, fresh[0], "golem_p1", ids.childModel(), ids.relation())
	if (PolicyRelationAliasFact{}).Matches("golem_p1") {
		t.Fatal("zero/forged alias fact matched a renderer alias")
	}
}

func TestPolicyAliasFactContainsOnlyOpaqueAliasAndStableIdentities(t *testing.T) {
	typ := reflect.TypeOf(PolicyRelationAliasFact{})
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath == "" {
			t.Fatalf("policy alias fact exposes mutable field %s", field.Name)
		}
		if field.Type == reflect.TypeOf(physical.PhysicalName("")) {
			t.Fatalf("policy alias fact retained a physical name in %s", field.Name)
		}
	}
	for _, forbidden := range []string{"Alias", "Name", "SQL", "Table", "Column", "Predicate", "Arguments"} {
		if _, ok := typ.MethodByName(forbidden); ok {
			t.Fatalf("policy alias fact exposes forbidden %s method", forbidden)
		}
	}
}

func relationCondition(t *testing.T, model ir.ModelID, field ir.FieldID, relation ir.RelationID, target ir.ModelID, cardinality ir.RelationCardinality, child *ir.Condition) ir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(ir.OperatorRelationSome, operator.Shape{
		Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive,
		Cardinality: cardinality, HasChild: child != nil, Providers: ir.PortableProviders(),
	})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewRelation(model, field, relation, target, cardinality, ir.OperatorRelationSome, child, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func assertPolicyAliasFact(t *testing.T, fact PolicyRelationAliasFact, alias string, model ir.ModelID, relation ir.RelationID) {
	t.Helper()
	if !fact.Matches(alias) || fact.Matches("") || fact.Matches(alias+"_forged") {
		t.Fatalf("alias matcher did not recognize only %q", alias)
	}
	if fact.ModelID() != model || fact.RelationID() != relation {
		t.Fatalf("alias fact identities = (%x, %x), want (%x, %x)", fact.ModelID(), fact.RelationID(), model, relation)
	}
}
