package sql

import (
	"strings"
	"testing"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestSelectionConstraintDisjunctionRemainsGroupedUnderTargetIdentity(t *testing.T) {
	fixture := schematest.New(t)
	model := policyir.ModelID(fixture.Post)
	disjunction := mutationTestDisjunction(t, model)
	truth, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		operation mutationir.Operation
		action    policyir.Action
	}{
		{"update", mutationir.Update, policyir.ActionUpdate},
		{"delete", mutationir.Delete, policyir.ActionDelete},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := target(t, fixture, uuidValue(73), nil)
			selection, err := mutationir.NewSelectionRequirement(test.action, disjunction)
			if err != nil {
				t.Fatal(err)
			}
			image, err := mutationir.NewImageRequirements(model, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.PostTitle)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			input := mutationir.NodeInput{Operation: test.operation, Model: model, Target: &target, Before: image, Selection: &selection, Identity: mutationir.IdentityUnchanged}
			if test.operation == mutationir.Update {
				input.ScalarOperations = []mutationir.ScalarOperation{setString(t, fixture, fixture.PostTitle, "grouped")}
				input.After = image
				input.RowPostcondition = &truth
			}
			graph, err := mutationir.NewGraph(input)
			if err != nil {
				t.Fatal(err)
			}
			program, err := Render(plan(t, graph, image), fixture.Registry, policyir.ProviderSQLite, testProof(t, fixture, policyir.ProviderSQLite))
			if err != nil {
				t.Fatal(err)
			}
			checked := 0
			for _, statement := range program.Statements() {
				if statement.Role() == VerifyPostcondition {
					continue
				}
				sql := statement.SQL()
				whereAt := strings.Index(sql, " WHERE ")
				if whereAt < 0 || !strings.Contains(sql, " OR ") {
					continue
				}
				assertMutationDisjunctionsNested(t, sql[whereAt+len(" WHERE "):])
				checked++
			}
			if checked == 0 {
				t.Fatalf("no statement carried the selection disjunction: %#v", program.Statements())
			}
		})
	}
}

func TestPersistedVerificationDisjunctionRemainsGroupedUnderPrimaryIdentity(t *testing.T) {
	fixture := schematest.New(t)
	model := policyir.ModelID(fixture.Post)
	disjunction := mutationTestDisjunction(t, model)
	truth, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := mutationir.NewImageRequirements(model, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID), policyir.FieldID(fixture.PostTitle)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Create, Model: model, ScalarOperations: []mutationir.ScalarOperation{setString(t, fixture, fixture.PostTitle, "created")}, After: after, RowPostcondition: &truth, Identity: mutationir.IdentityProduced})
	if err != nil {
		t.Fatal(err)
	}
	row, err := mutationdecode.NewRow(fixture.Registry, model, []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.PostID), uuidValue(74))})
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		verification, err := RenderPersistedVerification(graph.Nodes()[0], row, []policyir.Condition{disjunction, disjunction}, fixture.Registry, provider, testProof(t, fixture, provider))
		if err != nil {
			t.Fatal(err)
		}
		sql := verification.SQL()
		assertMutationDisjunctionsNested(t, sql[strings.Index(sql, " WHERE ")+len(" WHERE "):])
	}
}

func mutationTestDisjunction(t *testing.T, model policyir.ModelID) policyir.Condition {
	t.Helper()
	truth, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	falsehood, err := policyir.NewConstant(model, false)
	if err != nil {
		t.Fatal(err)
	}
	disjunction, err := policyir.NewLogical(model, policyir.LogicalOr, []policyir.Condition{falsehood, truth})
	if err != nil {
		t.Fatal(err)
	}
	return disjunction
}

func assertMutationDisjunctionsNested(t *testing.T, fragment string) {
	t.Helper()
	depth, found := 0, false
	for index := 0; index < len(fragment); index++ {
		switch fragment[index] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if strings.HasPrefix(fragment[index:], " OR ") {
			found = true
			if depth < 1 {
				t.Fatalf("disjunction escaped the mutation identity predicate: %s", fragment)
			}
		}
	}
	if !found {
		t.Fatalf("fragment has no disjunction to check: %s", fragment)
	}
}
