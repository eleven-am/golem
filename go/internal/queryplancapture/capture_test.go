package queryplancapture

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func fixedID[T ~[16]byte](value byte) T {
	var result T
	for index := range result {
		result[index] = value
	}
	return result
}

func TestAliasMapIsOpaqueExactAndAmbiguityFailClosed(t *testing.T) {
	model := fixedID[golem.ModelID](1)
	field := fixedID[golem.FieldID](2)
	fact, err := NewAliasFact(func(candidate string) bool { return candidate == "private_alias" }, model, golem.RelationID{}, []golem.FieldID{field}, AliasPhysicalAccess)
	if err != nil {
		t.Fatal(err)
	}
	plan := NewAliasMap(fact)
	identity, status := plan.Resolve("private_alias")
	if status != MatchExact || identity.ModelID() != model {
		t.Fatalf("exact identity mismatch: status=%v identity=%v", status, identity.ModelID())
	}
	fields := identity.FieldIDs()
	fields[0] = golem.FieldID{}
	if got := identity.FieldIDs(); len(got) != 1 || got[0] != field {
		t.Fatalf("field identity leaked mutable ownership: %v", got)
	}
	if _, status := plan.Resolve("PRIVATE_ALIAS"); status != MatchUnknown {
		t.Fatalf("unknown alias guessed: %v", status)
	}
	if _, status := NewAliasMap(fact, fact).Resolve("private_alias"); status != MatchAmbiguous {
		t.Fatalf("ambiguous alias guessed: %v", status)
	}
}

func TestPlanBoundsAndReportHandoffAreClosed(t *testing.T) {
	model := fixedID[golem.ModelID](3)
	identity, ok := PhysicalIdentity(model)
	if !ok {
		t.Fatal("physical identity rejected")
	}
	root := Access(AccessFullScan, identity, IndexID{})
	plan, err := NewPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NodeCount() != 1 || plan.RootInput().Kind != "access" || plan.RootInput().Access != "fullScan" {
		t.Fatalf("closed handoff mismatch: %#v", plan.RootInput())
	}
	for depth := 0; depth < MaxDepth; depth++ {
		root = Structural(NodeSort, AliasIdentity{}, root)
	}
	if _, err := NewPlan(root); codeOf(err) != ErrorTooComplex {
		t.Fatalf("depth overflow: %v", err)
	}
	if strings.Contains(Refuse(ErrorUnavailable).Error(), "provider") {
		t.Fatal("fixed refusal unexpectedly contains provider diagnostics")
	}
}

func TestRenderedReadSQLBoundaryRejectsAdditionalStatementsAndComments(t *testing.T) {
	for _, statement := range []string{"", "DELETE FROM private", "SELECT 1; SELECT private", "SELECT 1; DELETE FROM private", "SELECT 1 -- private", "WITH private AS (SELECT 1) DELETE FROM private", "SELECT /* private */ 1", "SELECT\x00 1"} {
		if ValidRenderedReadSQL(statement) {
			t.Fatalf("unsafe statement accepted: %q", statement)
		}
	}
	for _, statement := range []string{"SELECT 1", "  SELECT\n1", "WITH private AS (SELECT 1) SELECT * FROM private"} {
		if !ValidRenderedReadSQL(statement) {
			t.Fatalf("rendered read rejected: %q", statement)
		}
	}
}

func codeOf(err error) ErrorCode {
	code, _ := CodeOf(err)
	return code
}
