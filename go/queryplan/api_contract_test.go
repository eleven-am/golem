package queryplan_test

import (
	"encoding/json"
	"go/types"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/queryplan"
	"golang.org/x/tools/go/packages"
)

func TestQueryPlanPublicCoreSurfaceMatchesAcceptedContract(t *testing.T) {
	reportValue := reflect.TypeOf(queryplan.Report{})
	reportPointer := reflect.TypeOf(&queryplan.Report{})
	for _, forbidden := range []string{"MarshalJSON", "UnmarshalJSON"} {
		if _, ok := reportValue.MethodByName(forbidden); ok {
			t.Fatalf("Report unexpectedly exports %s", forbidden)
		}
		if _, ok := reportPointer.MethodByName(forbidden); ok {
			t.Fatalf("*Report unexpectedly exports %s", forbidden)
		}
	}
	if _, ok := any(queryplan.Report{}).(json.Marshaler); ok {
		t.Fatal("Report exposes an unaccepted public JSON wire format")
	}
	if _, ok := any(&queryplan.Report{}).(json.Unmarshaler); ok {
		t.Fatal("*Report exposes an unaccepted public JSON wire format")
	}

	node := reflect.TypeOf(queryplan.Node{})
	want := []string{"Access", "BatchCapacity", "Children", "FieldIDs", "IndexID", "Kind", "MaximumExecutionStatements", "MinimumExecutionStatements", "ModelID", "RelationID", "Warnings"}
	got := make([]string, node.NumMethod())
	for index := 0; index < node.NumMethod(); index++ {
		got[index] = node.Method(index).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Node methods=%v want=%v", got, want)
	}
}

func TestQueryPlanPublicAPIContainsOnlyAcceptedTypesConstantsAndAccessors(t *testing.T) {
	loaded, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedTypes}, ".")
	if err != nil || len(loaded) != 1 || len(loaded[0].Errors) != 0 {
		t.Fatalf("load queryplan: packages=%d err=%v errors=%v", len(loaded), err, loaded[0].Errors)
	}
	scope := loaded[0].Types.Scope()
	wantTypes := stringSet("AccessKind", "ErrorCode", "IndexID", "Node", "NodeKind", "Operation", "Report", "Statement", "StatementPurpose", "Warning")
	wantFunctions := stringSet("CodeOf")
	wantConstants := stringSet(
		"AccessKindNone", "AccessKindPrimaryKey", "AccessKindUniqueIndex", "AccessKindIndex", "AccessKindBitmapIndex", "AccessKindFullScan", "AccessKindConstant", "AccessKindUnknown",
		"ErrorUnavailable", "ErrorTooComplex", "ErrorInvalid",
		"NodeKindAccess", "NodeKindJoin", "NodeKindSort", "NodeKindAggregate", "NodeKindMaterialize", "NodeKindCorrelatedRelation", "NodeKindDeferredBatch", "NodeKindConstant", "NodeKindUnknown",
		"OperationFindUnique", "OperationFindFirst", "OperationFindMany", "OperationCount", "OperationAggregate", "OperationGroupBy", "OperationRelationGroupBy", "OperationScoped",
		"StatementPurposeRoot", "StatementPurposeRelationBatch", "StatementPurposePolicyHydration", "StatementPurposeAnalytics", "StatementPurposeScoped",
		"WarningFullScan", "WarningTemporarySort", "WarningMaterialization", "WarningDeferredBatch", "WarningMultiStatement", "WarningUnknownProviderNode",
	)
	for _, name := range scope.Names() {
		object := scope.Lookup(name)
		if !object.Exported() {
			continue
		}
		rendered := types.TypeString(object.Type(), func(pkg *types.Package) string { return pkg.Path() })
		if strings.Contains(rendered, "/internal/") {
			t.Fatalf("%s leaks internal type: %s", name, rendered)
		}
		switch object.(type) {
		case *types.TypeName:
			if !wantTypes[name] {
				t.Fatalf("unexpected exported type %s", name)
			}
			delete(wantTypes, name)
		case *types.Func:
			if !wantFunctions[name] {
				t.Fatalf("unexpected exported function %s", name)
			}
			delete(wantFunctions, name)
		case *types.Const:
			if !wantConstants[name] {
				t.Fatalf("unexpected exported constant %s", name)
			}
			delete(wantConstants, name)
		default:
			t.Fatalf("unexpected exported object %s (%T)", name, object)
		}
	}
	if len(wantTypes) != 0 || len(wantFunctions) != 0 || len(wantConstants) != 0 {
		t.Fatalf("missing public inventory: types=%v functions=%v constants=%v", sortedKeys(wantTypes), sortedKeys(wantFunctions), sortedKeys(wantConstants))
	}

	assertMethods(t, reflect.TypeOf(queryplan.Report{}), "CanonicalDigest", "FormatVersion", "MaximumExecutionStatements", "MinimumExecutionStatements", "Operation", "Provider", "RootModelID", "Statements", "Warnings")
	assertMethods(t, reflect.TypeOf(queryplan.Statement{}), "Ordinal", "Purpose", "Root")
	assertMethods(t, reflect.TypeOf(queryplan.Node{}), "Access", "BatchCapacity", "Children", "FieldIDs", "IndexID", "Kind", "MaximumExecutionStatements", "MinimumExecutionStatements", "ModelID", "RelationID", "Warnings")
}

func assertMethods(t *testing.T, value reflect.Type, want ...string) {
	t.Helper()
	for _, candidate := range []reflect.Type{value, reflect.PointerTo(value)} {
		got := make([]string, candidate.NumMethod())
		for index := range got {
			got[index] = candidate.Method(index).Name
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s methods=%v want=%v", candidate, got, want)
		}
	}
}

func stringSet(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func sortedKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
