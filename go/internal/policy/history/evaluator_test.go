package history_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

func TestEvaluatorBoundsRepeatedScalarMaterializationAcrossCandidates(t *testing.T) {
	fixture, input := graphInput(t)
	text := strings.Repeat("x", 120_000)
	for index, id := range []byte{1, 2} {
		input.Rows[index].Row = graphUser(t, fixture, id, text)
		input.Rows[index+3].Row = graphUser(t, fixture, id, text)
	}
	graph := evaluationReadback(t, fixture.Registry, input)
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	owner, err := graph.NewEvaluator(context.Background(), fixture.Registry, providers, history.Before)
	if err != nil {
		t.Fatal(err)
	}
	key, err := history.RowKey(fixture.Registry, graph.Provider(), input.Roots[0].Identity)
	if err != nil {
		t.Fatal(err)
	}
	condition := evaluationScalar(t, fixture.Registry, fixture.User, fixture.UserName, text, policyir.OperatorEqual)
	for range 8 {
		if matched, err := owner.EvaluateRoot(key, condition); err != nil || !matched {
			t.Fatalf("bounded candidate evaluation: matched=%v err=%v", matched, err)
		}
	}
	_, err = owner.EvaluateCaptured(key, condition)
	var limit *history.LimitError
	if !errors.As(err, &limit) || limit.Limit != golem.LimitPolicyHistoryBytes {
		t.Fatalf("candidate calls reset materialization bytes: %v", err)
	}
	if matched, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, condition); err != nil || !matched {
		t.Fatalf("one matching operation exhausted another's budget: matched=%v err=%v", matched, err)
	}
}

func TestEvaluatorSharesRelationProofBudgetWithConditionEvaluation(t *testing.T) {
	fixture, input := graphInput(t)
	graph := evaluationReadback(t, fixture.Registry, input)
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	owner, err := graph.NewEvaluator(context.Background(), fixture.Registry, providers, history.Before)
	if err != nil {
		t.Fatal(err)
	}
	key, err := history.RowKey(fixture.Registry, graph.Provider(), input.Rows[2].Row)
	if err != nil {
		t.Fatal(err)
	}
	name := evaluationScalar(t, fixture.Registry, fixture.User, fixture.UserName, "source", policyir.OperatorEqual)
	requirements, err := operator.ValidateShape(policyir.OperatorRelationIs, operator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: policyir.RelationToOne, HasChild: true, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	author, err := policyir.NewRelation(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship), policyir.ModelID(fixture.User), policyir.RelationToOne, policyir.OperatorRelationIs, &name, requirements)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for completed < history.MaxNodes {
		rows, complete, proofErr := owner.CertifiedChildren(key, policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship))
		if proofErr != nil {
			err = proofErr
			break
		}
		if !complete || len(rows) != 1 {
			t.Fatal("certified same-phase inverse became unknown")
		}
		matched, evalErr := owner.EvaluateCaptured(key, author)
		if evalErr != nil {
			err = evalErr
			break
		}
		if !matched {
			t.Fatal("historical inverse changed between shared calls")
		}
		completed++
	}
	var limit *history.LimitError
	if completed == 0 || completed >= history.MaxNodes || !errors.As(err, &limit) || limit.Limit != golem.LimitPolicyHistoryNodes {
		t.Fatalf("relation proof work reset between candidates: completed=%d err=%v", completed, err)
	}
	if matched, err := graph.EvaluateCaptured(context.Background(), fixture.Registry, providers, history.Before, key, author); err != nil || !matched {
		t.Fatalf("shared owner changed immutable evidence: matched=%v err=%v", matched, err)
	}
}

func TestEvaluatorBoundsRepeatedCompleteExtentCopies(t *testing.T) {
	fixture, input := graphInput(t)
	// Repeatedly enumerating a wide, complete owner extent is real work even
	// without inference or policy evaluation. It shares the matching budget.
	for index := range 500 {
		id := byte(index%200 + 52)
		row := graphPost(t, fixture, id, 1, "sibling")
		// Use the full UUID, keeping the short fixture helper for other cells.
		cells := row.Cells()
		for cell := range cells {
			if cells[cell].FieldID() == policyir.FieldID(fixture.PostID) {
				cells[cell] = mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{13: byte(index >> 8), 14: byte(index), 15: id}))
			}
		}
		input.Rows = append(input.Rows, history.PhaseRow{Phase: history.Before, Row: graphRow(t, fixture.Registry, fixture.Post, cells...)})
		input.Extents[0].Children = append(input.Extents[0].Children, history.RowRef(len(input.Rows)))
	}
	graph := evaluationReadback(t, fixture.Registry, input)
	owner, err := graph.NewEvaluator(context.Background(), fixture.Registry, policysql.SchemaResolver(fixture.Registry).Providers(), history.Before)
	if err != nil {
		t.Fatal(err)
	}
	key, err := history.RowKey(fixture.Registry, graph.Provider(), input.Roots[0].Identity)
	if err != nil {
		t.Fatal(err)
	}
	var completed int
	for completed < 100 {
		rows, complete, nextErr := owner.CertifiedChildren(key, policyir.FieldID(fixture.UserPosts), policyir.RelationID(fixture.Authorship))
		if nextErr != nil {
			err = nextErr
			break
		}
		if !complete || len(rows) != 501 {
			t.Fatalf("complete sibling extent changed: complete=%v rows=%d", complete, len(rows))
		}
		completed++
	}
	var limit *history.LimitError
	if completed == 0 || completed >= 100 || !errors.As(err, &limit) || limit.Limit != golem.LimitPolicyHistoryNodes {
		t.Fatalf("complete extent copies evaded the aggregate bound: copies=%d err=%v", completed, err)
	}
}
