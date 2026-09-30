package nested

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestRelatedRowDisjunctionsRemainGroupedUnderParentCorrelation(t *testing.T) {
	fixture := schematest.NewGraph(t)
	model := policyir.ModelID(fixture.Post)
	falsehood, err := policyir.NewConstant(model, false)
	if err != nil {
		t.Fatal(err)
	}
	truth, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	disjunction, err := policyir.NewLogical(model, policyir.LogicalOr, []policyir.Condition{falsehood, truth})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := golem.GeneratedModelDescriptor[relationSQLPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	title := golem.GeneratedTextField[relationSQLPost, string](fixture.PostTitle)
	frozen, err := title.Eq("open").Or(title.Eq("closed")).Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	predicate, err := mutationbind.BatchPredicate(frozen, fixture.Post, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	expansion, _ := mutationir.NewExpansionRequirement(mutationir.ExpandRelatedPredicate, 5)
	related, err := mutationir.NewRelationPosition(mutationir.RelationPositionInput{
		ParentModel: policyir.ModelID(fixture.User), Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship),
		TargetModel: model, Kind: mutationir.PositionRelatedPredicate, Predicate: &predicate, Expansion: &expansion,
	})
	if err != nil {
		t.Fatal(err)
	}
	selector, _ := mutationir.NewSelectorValue(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 9}))
	guarded, err := mutationir.NewTarget(model, fixture.PostKey, []mutationir.SelectorValue{selector}, &disjunction)
	if err != nil {
		t.Fatal(err)
	}
	unguarded := targetFor(t, fixture.Post, fixture.PostKey, fixture.PostID, 9)
	selection, err := mutationir.NewSelectionRequirement(policyir.ActionDelete, disjunction)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]mutationir.Node{
		"related-predicate": relationNode(t, fixture.User, fixture.UserKey, fixture.UserID, fixture.Post, fixture.Authorship, mutationir.DeleteMany, related),
		"target-guard":      relationNode(t, fixture.User, fixture.UserKey, fixture.UserID, fixture.Post, fixture.Authorship, mutationir.Delete, relationPosition(t, fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, mutationir.PositionRelatedTarget, &guarded)),
		"selection":         selectedRelationNode(t, fixture, relationPosition(t, fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, mutationir.PositionRelatedTarget, &unguarded), selection),
	}
	for name, node := range cases {
		for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
			t.Run(fmt.Sprintf("%s/%d", name, provider), func(t *testing.T) {
				program, err := RenderRelationExpansion(RelationExpansionSQLRequest{
					Node: node, Anchor: graphUserRow(t, fixture, 7), Registry: fixture.Registry, Provider: provider,
					Capabilities: relationProof(t, fixture.Registry, provider), MaxRows: 5, MaxParameters: 20,
				})
				if err != nil {
					t.Fatal(err)
				}
				statement := program.Statements()[0]
				where := strings.SplitN(statement.SQL(), " WHERE ", 2)
				if len(where) != 2 || !strings.Contains(where[1], "author_id") {
					t.Fatalf("related row query is not correlated: %s", statement.SQL())
				}
				depth, found := 0, false
				for index := 0; index < len(where[1]); index++ {
					switch where[1][index] {
					case '(':
						depth++
					case ')':
						depth--
					}
					if strings.HasPrefix(where[1][index:], " OR ") {
						found = true
						if depth < 1 {
							t.Fatalf("disjunction escaped the parent correlation: %s", statement.SQL())
						}
					}
				}
				if !found {
					t.Fatalf("related row query has no disjunction: %s", statement.SQL())
				}
			})
		}
	}
}

func selectedRelationNode(t *testing.T, fixture schematest.GraphFixture, position mutationir.RelationPosition, selection mutationir.SelectionRequirement) mutationir.Node {
	t.Helper()
	rootTarget := targetFor(t, fixture.User, fixture.UserKey, fixture.UserID, 7)
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Update, Model: policyir.ModelID(fixture.User), Target: &rootTarget, Identity: mutationir.IdentityUnchanged, Children: []mutationir.NodeInput{{
		Operation: mutationir.Delete, Model: policyir.ModelID(fixture.Post), Relation: policyir.RelationID(fixture.Authorship), RelationPosition: &position, Selection: &selection, Identity: mutationir.IdentityUnchanged,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return graph.Nodes()[1]
}
