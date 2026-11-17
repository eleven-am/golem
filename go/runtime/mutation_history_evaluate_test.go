package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

func TestMutationHistoryEvaluationUsesBothPhasesAndIndependentQuantifiers(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	fixture.mutate(t, golem.EventCreated, 51, 1, "old")
	fixture.mutate(t, golem.EventCreated, 52, 1, "other")
	target := golem.GeneratedUniqueSelectorValue[policyFactRuntimePost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 51}))
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if _, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, target, golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "new"))); err != nil {
			return err
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		graph, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		root := mutationHistoryEvaluationRoot(t, graph, fixture.schema.UserID, 1)
		old := mutationHistoryEvaluationTitle(t, fixture, "old", policyir.OperatorEqual)
		newTitle := mutationHistoryEvaluationTitle(t, fixture, "new", policyir.OperatorEqual)
		other := mutationHistoryEvaluationTitle(t, fixture, "other", policyir.OperatorEqual)
		someOld := mutationHistoryEvaluationPosts(t, fixture, policyir.OperatorRelationSome, old)
		noneOld := mutationHistoryEvaluationPosts(t, fixture, policyir.OperatorRelationNone, old)
		everyNew := mutationHistoryEvaluationPosts(t, fixture, policyir.OperatorRelationEvery, newTitle)
		everyNotOld := mutationHistoryEvaluationPosts(t, fixture, policyir.OperatorRelationEvery, mutationHistoryEvaluationTitle(t, fixture, "old", policyir.OperatorNotEqual))
		notOld, err := policyir.NewLogical(policyir.ModelID(fixture.schema.User), policyir.LogicalNot, []policyir.Condition{someOld})
		if err != nil {
			return err
		}
		independent, err := policyir.NewLogical(policyir.ModelID(fixture.schema.User), policyir.LogicalAnd, []policyir.Condition{
			mutationHistoryEvaluationPosts(t, fixture, policyir.OperatorRelationSome, newTitle),
			mutationHistoryEvaluationPosts(t, fixture, policyir.OperatorRelationSome, other),
		})
		if err != nil {
			return err
		}
		for _, test := range []struct {
			name          string
			condition     policyir.Condition
			before, after bool
		}{
			{"some", someOld, true, false},
			{"none", noneOld, false, true},
			{"every visits all children", everyNew, false, false},
			{"every negative", everyNotOld, false, true},
			{"not", notOld, false, true},
			{"independent same-relation branches", independent, false, true},
		} {
			for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
				want := test.before
				if phase == mutationHistoryAfter {
					want = test.after
				}
				got, err := graph.evaluate(fixture.ctx, phase, root, test.condition)
				if err != nil || got != want {
					t.Fatalf("%s phase=%d got=%v want=%v err=%v", test.name, phase, got, want, err)
				}
			}
		}
		// A relation not in the static inventory remains unknown even when
		// a Boolean branch would otherwise bypass it. It cannot become a
		// vacuously true negative predicate.
		truth, err := policyir.NewConstant(policyir.ModelID(fixture.schema.Comment), true)
		if err != nil {
			return err
		}
		comments := mutationHistoryEvaluationRelation(t, policyir.ModelID(fixture.schema.Post), policyir.FieldID(fixture.schema.PostComments), policyir.RelationID(fixture.schema.Commenting), policyir.ModelID(fixture.schema.Comment), policyir.OperatorRelationNone, truth)
		missing := mutationHistoryEvaluationPosts(t, fixture, policyir.OperatorRelationSome, comments)
		always, err := policyir.NewConstant(policyir.ModelID(fixture.schema.User), true)
		if err != nil {
			return err
		}
		bypass, err := policyir.NewLogical(policyir.ModelID(fixture.schema.User), policyir.LogicalOr, []policyir.Condition{always, missing})
		if err != nil {
			return err
		}
		if _, err := graph.evaluate(fixture.ctx, mutationHistoryAfter, root, bypass); err == nil {
			t.Fatal("true branch hid an unknown historical relation extent")
		}
		cancelled, cancel := context.WithCancel(fixture.ctx)
		cancel()
		if _, err := graph.evaluate(cancelled, mutationHistoryAfter, root, someOld); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled historical evaluation: %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMutationHistoryEvaluationKnowsEmptyRelationsBeforeReparent(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	fixture.mutate(t, golem.EventCreated, 51, 1, "ready")
	target := golem.GeneratedUniqueSelectorValue[policyFactRuntimePost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 51}))
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if _, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, target, golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))); err != nil {
			return err
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		graph, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		for _, id := range []byte{1, 2} {
			root := mutationHistoryEvaluationRoot(t, graph, fixture.schema.UserID, id)
			child := mutationHistoryEvaluationTitle(t, fixture, "ready", policyir.OperatorEqual)
			for _, operation := range []policyir.OperatorID{policyir.OperatorRelationSome, policyir.OperatorRelationNone, policyir.OperatorRelationEvery} {
				condition := mutationHistoryEvaluationPosts(t, fixture, operation, child)
				for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
					hasChild := id == 1 && phase == mutationHistoryBefore || id == 2 && phase == mutationHistoryAfter
					want := hasChild
					if operation == policyir.OperatorRelationNone {
						want = !hasChild
					}
					if operation == policyir.OperatorRelationEvery {
						want = true // Both ready rows and complete empty extents satisfy Every.
					}
					got, err := graph.evaluate(fixture.ctx, phase, root, condition)
					if err != nil || got != want {
						t.Fatalf("root=%d phase=%d operator=%d got=%v want=%v err=%v", id, phase, operation, got, want, err)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMutationHistoryEvaluationBoundsDeepWideMaterialization(t *testing.T) {
	const chainDepth, width = 30, 512
	fixture := mutationHistoryEvaluationRecursiveFixture(t, chainDepth+1)
	model := policyir.ModelID(fixture.Comment)
	input := policyhistory.Input{}
	text, err := policyir.StringValue("ready")
	if err != nil {
		t.Fatal(err)
	}
	identifier := func(id int) policyir.Value {
		return policyir.UUIDValue([16]byte{14: byte(id >> 8), 15: byte(id)})
	}
	add := func(id, parent int) policyhistory.RowRef {
		parentCell := mutationdecode.Null(policyir.FieldID(fixture.ParentID))
		if parent != 0 {
			parentCell = mutationdecode.Value(policyir.FieldID(fixture.ParentID), identifier(parent))
		}
		row, err := mutationdecode.NewRow(fixture.Registry, model, []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(fixture.CommentID), identifier(id)), parentCell,
			mutationdecode.Value(policyir.FieldID(fixture.Body), text),
		})
		if err != nil {
			t.Fatal(err)
		}
		input.Rows = append(input.Rows, policyhistory.PhaseRow{Phase: policyhistory.After, Row: row})
		return policyhistory.RowRef(len(input.Rows))
	}
	rootKey := add(1, 0)
	parentKey := rootKey
	for id := 2; id <= chainDepth+1; id++ {
		key := add(id, id-1)
		input.Extents = append(input.Extents, policyhistory.Extent{Phase: policyhistory.After, Parent: parentKey, Field: policyir.FieldID(fixture.Replies), Relation: policyir.RelationID(fixture.Threading), Children: []policyhistory.RowRef{key}})
		parentKey = key
	}
	var leaves []policyhistory.RowRef
	for id := chainDepth + 2; id < chainDepth+2+width; id++ {
		leaves = append(leaves, add(id, chainDepth+1))
	}
	input.Extents = append(input.Extents, policyhistory.Extent{Phase: policyhistory.After, Parent: parentKey, Field: policyir.FieldID(fixture.Replies), Relation: policyir.RelationID(fixture.Threading), Children: leaves})
	identity, err := mutationdecode.NewRow(fixture.Registry, model, []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.CommentID), identifier(1))})
	if err != nil {
		t.Fatal(err)
	}
	input.Roots = []policyhistory.Root{{Identity: identity, After: policyhistory.Presence{Present: true, Row: rootKey}}}
	value, err := policyhistory.New(fixture.Registry, policyir.ProviderSQLite, input)
	if err != nil {
		t.Fatal(err)
	}
	graph := &mutationHistoryGraph{snapshot: &mutationHistorySnapshot{state: mutationHistoryState(t), registry: fixture.Registry, provider: policyir.ProviderSQLite}, value: value}
	key, err := policyhistory.RowKey(fixture.Registry, policyir.ProviderSQLite, identity)
	if err != nil {
		t.Fatal(err)
	}
	root := mutationHistoryRoot{model: model, key: key}
	condition, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	for depth := 0; depth <= chainDepth; depth++ {
		condition = mutationHistoryEvaluationRelation(t, model, policyir.FieldID(fixture.Replies), policyir.RelationID(fixture.Threading), model, policyir.OperatorRelationEvery, condition)
	}
	// This is fewer than 600 logical rows/fields, but constructing the
	// immutable nested evaluator records would clone the wide leaf set at
	// every ancestor. The work budget must reject that expansion.
	_, err = graph.evaluate(context.Background(), mutationHistoryAfter, root, condition)
	mutationHistoryLimit(t, err, golem.LimitPolicyHistoryNodes)
}

func TestMutationHistoryEvaluationBoundsRepeatedScalarsOnCycles(t *testing.T) {
	for _, test := range []struct {
		name         string
		depth, bytes int
		limited      bool
	}{
		{"finite cyclic paths", 3, 5, false},
		{"materialized scalar bytes", 31, 40 << 10, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := mutationHistoryEvaluationRecursiveFixture(t, test.depth)
			model := policyir.ModelID(fixture.Comment)
			text, err := policyir.StringValue(strings.Repeat("x", test.bytes))
			if err != nil {
				t.Fatal(err)
			}
			id := policyir.UUIDValue([16]byte{15: 1})
			identity, err := mutationdecode.NewRow(fixture.Registry, model, []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.CommentID), id)})
			if err != nil {
				t.Fatal(err)
			}
			row, err := mutationdecode.NewRow(fixture.Registry, model, []mutationdecode.Cell{
				mutationdecode.Value(policyir.FieldID(fixture.CommentID), id),
				mutationdecode.Value(policyir.FieldID(fixture.ParentID), id),
				mutationdecode.Value(policyir.FieldID(fixture.Body), text),
			})
			if err != nil {
				t.Fatal(err)
			}
			value, err := policyhistory.New(fixture.Registry, policyir.ProviderSQLite, policyhistory.Input{
				Rows:    []policyhistory.PhaseRow{{Phase: policyhistory.After, Row: row}},
				Roots:   []policyhistory.Root{{Identity: identity, After: policyhistory.Presence{Present: true, Row: 1}}},
				Extents: []policyhistory.Extent{{Phase: policyhistory.After, Parent: 1, Field: policyir.FieldID(fixture.Replies), Relation: policyir.RelationID(fixture.Threading), Children: []policyhistory.RowRef{1}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			providers, err := providerSet(fixture.Registry)
			if err != nil {
				t.Fatal(err)
			}
			descriptor, found := policysql.SchemaResolver(fixture.Registry).Field(policyir.ProviderSQLite, model, policyir.FieldID(fixture.Body))
			if !found {
				t.Fatal("body descriptor absent")
			}
			operand, err := policyir.OneOperand(text)
			if err != nil {
				t.Fatal(err)
			}
			requirements, err := operator.ValidateShape(policyir.OperatorEqual, operator.Shape{Node: policyir.ConditionScalar, FieldType: descriptor.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: providers})
			if err != nil {
				t.Fatal(err)
			}
			body, err := policyir.NewScalar(model, policyir.FieldID(fixture.Body), descriptor.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
			if err != nil {
				t.Fatal(err)
			}
			condition := body
			for depth := 0; depth < test.depth; depth++ {
				relation := mutationHistoryEvaluationRelation(t, model, policyir.FieldID(fixture.Replies), policyir.RelationID(fixture.Threading), model, policyir.OperatorRelationEvery, condition)
				condition, err = policyir.NewLogical(model, policyir.LogicalAnd, []policyir.Condition{body, relation})
				if err != nil {
					t.Fatal(err)
				}
			}
			graph := &mutationHistoryGraph{snapshot: &mutationHistorySnapshot{state: mutationHistoryState(t), registry: fixture.Registry, provider: policyir.ProviderSQLite}, value: value}
			key, err := policyhistory.RowKey(fixture.Registry, policyir.ProviderSQLite, identity)
			if err != nil {
				t.Fatal(err)
			}
			matched, err := graph.evaluate(context.Background(), mutationHistoryAfter, mutationHistoryRoot{model: model, key: key}, condition)
			if test.limited {
				mutationHistoryLimit(t, err, golem.LimitPolicyHistoryBytes)
			} else if err != nil || !matched {
				t.Fatalf("finite cyclic path failed: matched=%v err=%v", matched, err)
			}
		})
	}
}

func mutationHistoryEvaluationRoot(t *testing.T, graph *mutationHistoryGraph, field golem.FieldID, id byte) mutationHistoryRoot {
	t.Helper()
	for _, root := range graph.affectedRoots() {
		for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
			row, known := graph.root(phase, root.key)
			if !known || row == nil {
				continue
			}
			cell, _ := row.Cell(policyir.FieldID(field))
			value, _ := cell.PolicyValue()
			if got, _ := value.UUID(); got == (golem.UUID{15: id}.Bytes()) {
				return root
			}
		}
	}
	t.Fatalf("affected root %d was not captured", id)
	return mutationHistoryRoot{}
}

func mutationHistoryEvaluationTitle(t *testing.T, fixture policyFactRuntimeFixture, text string, operation policyir.OperatorID) policyir.Condition {
	t.Helper()
	value, err := policyir.StringValue(text)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := policyir.OneOperand(value)
	if err != nil {
		t.Fatal(err)
	}
	model, field := policyir.ModelID(fixture.schema.Post), policyir.FieldID(fixture.schema.PostTitle)
	descriptor, found := policysql.SchemaResolver(fixture.app.registry).Field(policyir.ProviderSQLite, model, field)
	if !found {
		t.Fatal("title descriptor absent")
	}
	requirements, err := operator.ValidateShape(operation, operator.Shape{Node: policyir.ConditionScalar, FieldType: descriptor.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: policyir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(model, field, descriptor.Type, operation, policyir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func mutationHistoryEvaluationPosts(t *testing.T, fixture policyFactRuntimeFixture, operation policyir.OperatorID, child policyir.Condition) policyir.Condition {
	t.Helper()
	return mutationHistoryEvaluationRelation(t, policyir.ModelID(fixture.schema.User), policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship), policyir.ModelID(fixture.schema.Post), operation, child)
}

func mutationHistoryEvaluationRelation(t *testing.T, model policyir.ModelID, field policyir.FieldID, relation policyir.RelationID, target policyir.ModelID, operation policyir.OperatorID, child policyir.Condition) policyir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(operation, operator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: policyir.RelationToMany, HasChild: true, Providers: policyir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewRelation(model, field, relation, target, policyir.RelationToMany, operation, &child, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func mutationHistoryEvaluationRecursiveFixture(t *testing.T, depth int) schematest.RecursiveCommentFixture {
	t.Helper()
	fixture := schematest.NewSubscribedRecursiveComment(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	model := compilerir.ModelID(hex.EncodeToString(fixture.Comment[:]))
	body, _ := fixture.Registry.Field(fixture.Comment, fixture.Body)
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "RecursiveArgs"}
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{{ModelID: model, Template: compilerir.ScopeTemplateIR{Name: "owner", Arguments: args, Expression: compilerir.ScopeExpressionIR{ModelID: model, Operator: "true"}}}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	expression := compilerir.ScopeExpressionIR{ModelID: model, Operator: "eq", FieldID: compilerir.FieldID(hex.EncodeToString(fixture.Body[:])), Parameter: "State"}
	for index := 0; index < depth; index++ {
		expression = compilerir.ScopeExpressionIR{ModelID: model, Operator: "every", FieldID: compilerir.FieldID(hex.EncodeToString(fixture.Replies[:])), RelationID: compilerir.RelationID(hex.EncodeToString(fixture.Threading[:])), TargetModelID: model, Children: []compilerir.ScopeExpressionIR{expression}}
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{{ModelID: model, ScopeName: "owner", Template: compilerir.FactTemplateIR{Name: "thread", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: body.LogicalType()}}, Expression: expression}}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), mutationHistoryRuntimeDigest(t, string(fingerprint)), payload)
	fixture.Bundle = golem.GeneratedSchemaBundle(fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), fixture.Bundle.Model(), contract, fixture.Bundle.Providers()...)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}
