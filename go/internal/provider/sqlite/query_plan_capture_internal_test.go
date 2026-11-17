package sqlite

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
)

func TestCorrelatedWrapperNormalizationRequiresProvenChild(t *testing.T) {
	fact, err := queryplancapture.NewAliasFact(func(candidate string) bool { return candidate == "child" }, golem.ModelID{15: 1}, golem.RelationID{15: 2}, nil, queryplancapture.AliasCorrelatedRelation)
	if err != nil {
		t.Fatal(err)
	}
	identity, status := queryplancapture.NewAliasMap(fact).Resolve("child")
	if status != queryplancapture.MatchExact {
		t.Fatal("missing fixture alias")
	}
	// A real unknown descendant must survive removal of the duplicate wrapper.
	child := queryplancapture.Structural(queryplancapture.NodeCorrelatedRelation, identity, queryplancapture.Unknown())
	for _, detail := range []string{"CORRELATED SCALAR SUBQUERY 1", "CORRELATED SCALAR SUBQUERY 123"} {
		got := parseSQLitePlanDetail(detail, []queryplancapture.Node{child}, nil, queryplancapture.AliasMap{})
		if !reflect.DeepEqual(got, child) {
			t.Fatalf("known wrapper changed proven subtree: %q", detail)
		}
	}
	for _, detail := range []string{"CORRELATED SCALAR SUBQUERY ", "CORRELATED SCALAR SUBQUERY +1", "CORRELATED SCALAR SUBQUERY -1", "CORRELATED SCALAR SUBQUERY 1 extra", "CORRELATED SCALAR SUBQUERY  1", "CORRELATED SCALAR SUBQUERY ١", "SCALAR SUBQUERY 1"} {
		got := parseSQLitePlanDetail(detail, []queryplancapture.Node{child}, nil, queryplancapture.AliasMap{})
		if got.Kind() != queryplancapture.NodeUnknown || !reflect.DeepEqual(got.Children(), []queryplancapture.Node{child}) {
			t.Fatalf("unknown wrapper lost its uncertainty or subtree: %q", detail)
		}
	}
	for _, children := range [][]queryplancapture.Node{
		nil, {child, child}, {queryplancapture.Unknown()},
		{queryplancapture.Access(queryplancapture.AccessFullScan, identity, queryplancapture.IndexID{})},
		{queryplancapture.Structural(queryplancapture.NodeCorrelatedRelation, queryplancapture.AliasIdentity{})},
		{queryplancapture.Structural(queryplancapture.NodeSort, identity)},
		{queryplancapture.Structural(queryplancapture.NodeMaterialize, identity)},
	} {
		got := parseSQLitePlanDetail("CORRELATED SCALAR SUBQUERY 1", children, nil, queryplancapture.AliasMap{})
		if got.Kind() != queryplancapture.NodeUnknown || len(got.Children()) != len(children) {
			t.Fatal("unproven child acquired correlated identity")
		}
	}
}
