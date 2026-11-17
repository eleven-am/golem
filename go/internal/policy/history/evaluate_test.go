package history_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// Reading the codec back gives these tests no live transaction, queryer or
// runtime loader. Producer and durable consumer use the same evaluator.
func TestGraphEvaluateSealedReadbackQuantifiers(t *testing.T) {
	fixture, input := graphInput(t)
	input.Rows = append(input.Rows,
		history.PhaseRow{Phase: history.Before, Row: graphPost(t, fixture, 52, 1, "other")},
		history.PhaseRow{Phase: history.After, Row: graphPost(t, fixture, 52, 2, "other")},
	)
	input.Extents[0].Children = append(input.Extents[0].Children, 7)
	input.Extents[3].Children = append(input.Extents[3].Children, 8)
	graph := evaluationReadback(t, fixture.Registry, input)
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	scalar := func(text string, operation policyir.OperatorID) policyir.Condition {
		return evaluationScalar(t, fixture.Registry, fixture.Post, fixture.PostTitle, text, operation)
	}
	posts := func(operation policyir.OperatorID, child policyir.Condition) policyir.Condition {
		return evaluationRelation(t, providers, fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, operation, child)
	}
	before := scalar("before", policyir.OperatorEqual)
	other := scalar("other", policyir.OperatorEqual)
	independent, err := policyir.NewLogical(policyir.ModelID(fixture.User), policyir.LogicalAnd, []policyir.Condition{
		posts(policyir.OperatorRelationSome, before), posts(policyir.OperatorRelationSome, other),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		condition policyir.Condition
		// Source 1 has before+other then empty; source 2 has empty then after+other.
		want [4]bool
	}{
		{"some", posts(policyir.OperatorRelationSome, before), [4]bool{true, false, false, false}},
		{"none", posts(policyir.OperatorRelationNone, before), [4]bool{false, true, true, true}},
		{"every visits every child", posts(policyir.OperatorRelationEvery, before), [4]bool{false, true, true, false}},
		{"every negative", posts(policyir.OperatorRelationEvery, scalar("missing", policyir.OperatorNotEqual)), [4]bool{true, true, true, true}},
		{"independent witnesses", independent, [4]bool{true, false, false, false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for source, root := range input.Roots {
				key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, root.Identity)
				if err != nil {
					t.Fatal(err)
				}
				for phaseIndex, phase := range []history.Phase{history.Before, history.After} {
					got, err := graph.Evaluate(context.Background(), fixture.Registry, providers, phase, key, test.condition)
					want := test.want[2*source+phaseIndex]
					if err != nil || got != want {
						t.Fatalf("source=%d phase=%d got=%v want=%v err=%v", source, phase, got, want, err)
					}
				}
			}
		})
	}
}

func TestGraphEvaluateKeepsLogicalAndRelationDepthSeparate(t *testing.T) {
	fixture, input := graphInput(t)
	graph := evaluationReadback(t, fixture.Registry, input)
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	key, err := history.RowKey(fixture.Registry, graph.Provider(), input.Roots[0].Identity)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewConstant(policyir.ModelID(fixture.User), true)
	if err != nil {
		t.Fatal(err)
	}
	// Expanded logical policy context adds no physical relation traversal.
	// It remains valid beyond the capture path's 64-hop depth bound.
	for range 96 {
		condition, err = policyir.NewLogical(policyir.ModelID(fixture.User), policyir.LogicalNot, []policyir.Condition{condition})
		if err != nil {
			t.Fatal(err)
		}
	}
	matched, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, condition)
	if err != nil || !matched {
		t.Fatalf("valid logical policy expansion was refused as a relation path: %v", err)
	}
}

func TestGraphEvaluateRefusesMissingEvidenceAndWrongProvenance(t *testing.T) {
	fixture, input := graphInput(t)
	graph := evaluationReadback(t, fixture.Registry, input)
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, input.Roots[0].Identity)
	if err != nil {
		t.Fatal(err)
	}
	truth, err := policyir.NewConstant(policyir.ModelID(fixture.User), true)
	if err != nil {
		t.Fatal(err)
	}
	commentTruth, err := policyir.NewConstant(policyir.ModelID(fixture.Comment), true)
	if err != nil {
		t.Fatal(err)
	}
	comments := evaluationRelation(t, providers, fixture.Post, fixture.PostComments, fixture.Commenting, fixture.Comment, policyir.OperatorRelationNone, commentTruth)
	missing := evaluationRelation(t, providers, fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, policyir.OperatorRelationSome, comments)
	bypass, err := policyir.NewLogical(policyir.ModelID(fixture.User), policyir.LogicalOr, []policyir.Condition{truth, missing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, bypass); err == nil {
		t.Fatal("true branch hid an unknown historical extent")
	}
	generation := fixture.Bundle.GenerationDigest()
	generation[0] ^= 0xff
	otherGeneration, err := schema.New(golem.GeneratedSchemaBundle(generation, fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), fixture.Bundle.Model(), fixture.Bundle.Contract(), fixture.Bundle.Providers()...))
	if err != nil {
		t.Fatal(err)
	}
	otherProjection := schematest.NewSubscribedGraph(t).Registry
	if otherProjection.GenerationDigest() != fixture.Registry.GenerationDigest() || otherProjection.PolicyCaptureFingerprint() == fixture.Registry.PolicyCaptureFingerprint() {
		t.Fatal("projection mismatch fixture must isolate the capture fingerprint")
	}
	for _, registry := range []*schema.Registry{nil, otherGeneration, otherProjection} {
		if _, err := graph.Evaluate(context.Background(), registry, providers, history.Before, key, truth); err == nil {
			t.Fatal("wrong producing schema accepted")
		}
	}
	postgresOnly, err := policyir.NewProviderSet(policyir.ProviderPostgreSQL)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []policyir.ProviderSet{0, postgresOnly} {
		if _, err := graph.Evaluate(context.Background(), fixture.Registry, invalid, history.Before, key, truth); err == nil {
			t.Fatal("incompatible provider set accepted")
		}
	}
	if _, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, "unknown", truth); err == nil {
		t.Fatal("unknown root became false")
	}
	if _, err := graph.Evaluate(context.Background(), fixture.Registry, providers, 0, key, truth); err == nil {
		t.Fatal("invalid phase accepted")
	}
	if _, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, commentTruth); err == nil {
		t.Fatal("condition root model mismatch accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := graph.Evaluate(cancelled, fixture.Registry, providers, history.Before, key, truth); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestGraphEvaluateKnownAbsentRootIsFalse(t *testing.T) {
	fixture, input := graphInput(t)
	input.Rows = input.Rows[3:]
	for index := range input.Roots {
		input.Roots[index].Before = history.Presence{}
		input.Roots[index].After.Row -= 3
	}
	input.Extents = input.Extents[2:]
	for index := range input.Extents {
		input.Extents[index].Parent -= 3
		for child := range input.Extents[index].Children {
			input.Extents[index].Children[child] -= 3
		}
	}
	graph := evaluationReadback(t, fixture.Registry, input)
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, input.Roots[0].Identity)
	if err != nil {
		t.Fatal(err)
	}
	truth, err := policyir.NewConstant(policyir.ModelID(fixture.User), true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, truth)
	if err != nil || got {
		t.Fatalf("known absent source: got=%v err=%v", got, err)
	}
	if _, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, history.Before, key, truth); err == nil {
		t.Fatal("known absent root became a present captured row")
	}
	wrong, err := policyir.NewConstant(policyir.ModelID(fixture.Post), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, wrong); err == nil {
		t.Fatal("absent root bypassed identity model validation")
	}
}

type evaluationSchemaResolver struct{ registry *schema.Registry }

func (resolver evaluationSchemaResolver) ResolvePolicyHistorySchema(generation, projection golem.SchemaDigest) (*schema.Registry, bool) {
	return resolver.registry, resolver.registry.GenerationDigest() == generation && resolver.registry.PolicyCaptureFingerprint() == projection
}
func evaluationReadback(t *testing.T, registry *schema.Registry, input history.Input) history.Graph {
	t.Helper()
	graph, err := history.New(registry, policyir.ProviderSQLite, input)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := mutationfact.NewPolicyHistory(mutationfact.CausationID{1}, golem.SchemaDigest{2}, graph)
	if err != nil {
		t.Fatal(err)
	}
	readback, err := mutationfact.DecodePolicyHistory(sealed.Encoded(), evaluationSchemaResolver{registry})
	if err != nil {
		t.Fatal(err)
	}
	return readback.Graph()
}
func evaluationScalar(t *testing.T, registry *schema.Registry, model golem.ModelID, field golem.FieldID, text string, operation policyir.OperatorID) policyir.Condition {
	t.Helper()
	operand, err := policyir.OneOperand(graphText(t, text))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, found := policysql.SchemaResolver(registry).Field(policyir.ProviderSQLite, policyir.ModelID(model), policyir.FieldID(field))
	if !found {
		t.Fatal("scalar descriptor absent")
	}
	requirements, err := operator.ValidateShape(operation, operator.Shape{Node: policyir.ConditionScalar, FieldType: descriptor.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: policysql.SchemaResolver(registry).Providers()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(policyir.ModelID(model), policyir.FieldID(field), descriptor.Type, operation, policyir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}
func evaluationRelation(t *testing.T, providers policyir.ProviderSet, model golem.ModelID, field golem.FieldID, relation golem.RelationID, target golem.ModelID, operation policyir.OperatorID, child policyir.Condition) policyir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(operation, operator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: policyir.RelationToMany, HasChild: true, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewRelation(policyir.ModelID(model), policyir.FieldID(field), policyir.RelationID(relation), policyir.ModelID(target), policyir.RelationToMany, operation, &child, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func TestGraphEvaluateRejectsForeignDescriptorsBeforeShortcuts(t *testing.T) {
	fixture, input := graphInput(t)
	present := evaluationReadback(t, fixture.Registry, input)
	absentInput := cloneGraphInput(input)
	absentInput.Rows = absentInput.Rows[3:]
	for index := range absentInput.Roots {
		absentInput.Roots[index].Before = history.Presence{}
		absentInput.Roots[index].After.Row -= 3
	}
	absentInput.Extents = absentInput.Extents[2:]
	for index := range absentInput.Extents {
		absentInput.Extents[index].Parent -= 3
		for child := range absentInput.Extents[index].Children {
			absentInput.Extents[index].Children[child] -= 3
		}
	}
	absent := evaluationReadback(t, fixture.Registry, absentInput)
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, input.Roots[0].Identity)
	if err != nil {
		t.Fatal(err)
	}
	model := policyir.ModelID(fixture.User)
	valid := evaluationScalar(t, fixture.Registry, fixture.User, fixture.UserName, "source", policyir.OperatorEqual)
	if got, err := present.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, valid); err != nil || !got {
		t.Fatalf("matching producer descriptor refused: matched=%v err=%v", got, err)
	}
	typ, _ := valid.FieldType()
	// Same ValueString and operand, but a different nullability contract.
	foreignType, err := policyir.NewTypeRef(typ.Kind(), !typ.Nullable(), 0, 0, policyir.EnumID{}, nil, typ.Capability())
	if err != nil {
		t.Fatal(err)
	}
	operand, _ := valid.Operand()
	requirements, err := operator.ValidateShape(policyir.OperatorEqual, operator.Shape{Node: policyir.ConditionScalar, FieldType: foreignType, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	foreignScalar, err := policyir.NewScalar(model, policyir.FieldID(fixture.UserName), foreignType, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	child, err := policyir.NewConstant(policyir.ModelID(fixture.Post), true)
	if err != nil {
		t.Fatal(err)
	}
	foreignRelationID := fixture.Authorship
	foreignRelationID[0] ^= 0xff
	foreignRelation := evaluationRelation(t, providers, fixture.User, fixture.UserPosts, foreignRelationID, fixture.Post, policyir.OperatorRelationSome, child)
	wrongTarget, err := policyir.NewConstant(policyir.ModelID(fixture.Comment), true)
	if err != nil {
		t.Fatal(err)
	}
	foreignTarget := evaluationRelation(t, providers, fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Comment, policyir.OperatorRelationSome, wrongTarget)
	requirements, err = operator.ValidateShape(policyir.OperatorRelationIs, operator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: policyir.RelationToOne, HasChild: true, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	foreignCardinality, err := policyir.NewRelation(model, policyir.FieldID(fixture.UserPosts), policyir.RelationID(fixture.Authorship), policyir.ModelID(fixture.Post), policyir.RelationToOne, policyir.OperatorRelationIs, &child, requirements)
	if err != nil {
		t.Fatal(err)
	}
	truth, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	falsity, err := policyir.NewConstant(model, false)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := policyir.NewFactScope(truth)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		name      string
		condition policyir.Condition
	}{
		{"same-kind foreign TypeRef", foreignScalar}, {"foreign relation identity", foreignRelation},
		{"foreign relation target", foreignTarget}, {"foreign relation cardinality", foreignCardinality},
	} {
		t.Run(bad.name, func(t *testing.T) {
			and, err := policyir.NewLogical(model, policyir.LogicalAnd, []policyir.Condition{falsity, bad.condition})
			if err != nil {
				t.Fatal(err)
			}
			or, err := policyir.NewLogical(model, policyir.LogicalOr, []policyir.Condition{truth, bad.condition})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := policyir.NewFactDefinition(model, "descriptor-check", bad.condition)
			if err != nil {
				t.Fatal(err)
			}
			fact, err := policyir.NewFact(model, definition.ID(), bad.condition, scope)
			if err != nil {
				t.Fatal(err)
			}
			for _, condition := range []policyir.Condition{bad.condition, and, or, fact} {
				for _, graph := range []history.Graph{present, absent} {
					if _, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, condition); err == nil || !strings.Contains(err.Error(), "producing schema") {
						t.Fatalf("descriptor disagreement escaped the pre-evaluation check: %v", err)
					}
				}
			}
		})
	}
}

func TestGraphEvaluateCapturedUsesPresentSamePhaseEvidence(t *testing.T) {
	fixture, input := graphInput(t)
	postAfterOnly := graphPost(t, fixture, 52, 2, "new")
	input.Rows = append(input.Rows, history.PhaseRow{Phase: history.After, Row: postAfterOnly})
	input.Extents[3].Children = append(input.Extents[3].Children, history.RowRef(len(input.Rows)))
	graph := evaluationReadback(t, fixture.Registry, input)
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, input.Rows[2].Row)
	if err != nil {
		t.Fatal(err)
	}
	before := evaluationScalar(t, fixture.Registry, fixture.Post, fixture.PostTitle, "before", policyir.OperatorEqual)
	if _, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, before); err == nil {
		t.Fatal("ordinary root evaluation unexpectedly admitted a non-root row")
	}
	for _, phase := range []history.Phase{history.Before, history.After} {
		got, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, phase, key, before)
		if err != nil || got != (phase == history.Before) {
			t.Fatalf("phase=%d matched=%v err=%v", phase, got, err)
		}
	}
	name := evaluationScalar(t, fixture.Registry, fixture.User, fixture.UserName, "source", policyir.OperatorEqual)
	requirements, err := operator.ValidateShape(policyir.OperatorRelationIs, operator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: policyir.RelationToOne, HasChild: true, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	inverse, err := policyir.NewRelation(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship), policyir.ModelID(fixture.User), policyir.RelationToOne, policyir.OperatorRelationIs, &name, requirements)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []history.Phase{history.Before, history.After} {
		if got, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, phase, key, inverse); err != nil || !got {
			t.Fatalf("captured inverse phase=%d matched=%v err=%v", phase, got, err)
		}
	}
	afterKey, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, postAfterOnly)
	if err != nil {
		t.Fatal(err)
	}
	newTitle := evaluationScalar(t, fixture.Registry, fixture.Post, fixture.PostTitle, "new", policyir.OperatorEqual)
	if got, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, history.After, afterKey, newTitle); err != nil || !got {
		t.Fatalf("after-only row: matched=%v err=%v", got, err)
	}
	if _, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, history.Before, afterKey, newTitle); err == nil {
		t.Fatal("other phase supplied missing row")
	}
	if _, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, history.After, "missing", newTitle); err == nil {
		t.Fatal("missing captured key became false")
	}
	if _, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, history.Before, key, name); err == nil {
		t.Fatal("captured model mismatch accepted")
	}
}

func TestGraphEvaluateCapturedRetainsProvenanceAndDescriptorChecks(t *testing.T) {
	fixture, input := graphInput(t)
	graph := evaluationReadback(t, fixture.Registry, input)
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, input.Rows[2].Row)
	if err != nil {
		t.Fatal(err)
	}
	condition := evaluationScalar(t, fixture.Registry, fixture.Post, fixture.PostTitle, "before", policyir.OperatorEqual)
	generation := fixture.Bundle.GenerationDigest()
	generation[0] ^= 0xff
	other, err := schema.New(golem.GeneratedSchemaBundle(generation, fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), fixture.Bundle.Model(), fixture.Bundle.Contract(), fixture.Bundle.Providers()...))
	if err != nil {
		t.Fatal(err)
	}
	for _, registry := range []*schema.Registry{nil, other, schematest.NewSubscribedGraph(t).Registry} {
		if _, err := graph.EvaluateCaptured(context.Background(), registry, providers, history.Before, key, condition); err == nil {
			t.Fatal("captured lookup accepted wrong provenance")
		}
	}
	if _, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, 0, history.Before, key, condition); err == nil {
		t.Fatal("invalid providers accepted")
	}
	if _, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, 0, key, condition); err == nil {
		t.Fatal("invalid phase accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := graph.EvaluateCaptured(cancelled, fixture.Registry, providers, history.Before, key, condition); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	typ, _ := condition.FieldType()
	foreign, err := policyir.NewTypeRef(typ.Kind(), !typ.Nullable(), 0, 0, policyir.EnumID{}, nil, typ.Capability())
	if err != nil {
		t.Fatal(err)
	}
	operand, _ := condition.Operand()
	requirements, err := operator.ValidateShape(policyir.OperatorEqual, operator.Shape{Node: policyir.ConditionScalar, FieldType: foreign, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	invalid, err := policyir.NewScalar(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostTitle), foreign, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	truth, err := policyir.NewConstant(policyir.ModelID(fixture.Post), true)
	if err != nil {
		t.Fatal(err)
	}
	bypass, err := policyir.NewLogical(policyir.ModelID(fixture.Post), policyir.LogicalOr, []policyir.Condition{truth, invalid})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, history.Before, key, bypass); err == nil || !strings.Contains(err.Error(), "producing schema") {
		t.Fatalf("foreign captured descriptor hidden by true branch: %v", err)
	}
}
