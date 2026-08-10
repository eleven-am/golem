package sql

import (
	"testing"

	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestImageDependencyDistinguishesDirectFromRelationTraversal(t *testing.T) {
	model := policyir.ModelID{15: 1}
	field := policyir.FieldID{15: 2}
	direct, err := mutationir.NewDependency(model, nil, field)
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := mutationir.NewImageRequirements(model, []policyir.FieldID{field}, []mutationir.Dependency{direct})
	if err != nil {
		t.Fatal(err)
	}
	if hasDependencies(requirements) {
		t.Fatal("empty-path scalar dependency was mistaken for relation traversal")
	}

	target := policyir.ModelID{15: 3}
	hop, err := mutationir.NewRelationHop(model, policyir.FieldID{15: 4}, policyir.RelationID{15: 5}, target)
	if err != nil {
		t.Fatal(err)
	}
	traversing, err := mutationir.NewDependency(model, []mutationir.RelationHop{hop}, policyir.FieldID{15: 6})
	if err != nil {
		t.Fatal(err)
	}
	requirements, err = mutationir.NewImageRequirements(model, nil, []mutationir.Dependency{traversing})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDependencies(requirements) {
		t.Fatal("relation-traversing dependency was not retained")
	}
}
