package operation

import (
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
	"testing"
)

func TestPageTakeAdmissionUsesDeclaredDefaultAndBothMaximums(t *testing.T) {
	compiler := &Compiler{limits: Limits{Bind: graphqlbind.Limits{MaxPageSize: 40}}}
	operation := compilerir.CustomOperationContractIR{Name: "feed", Pagination: &compilerir.CustomPaginationIR{DefaultTake: 25, MaxTake: 100, MaxCandidates: 200}}
	for name, supplied := range map[string]map[string]any{"omitted": {}, "null": {"take": nil}, "explicit": {"take": int32(30)}} {
		t.Run(name, func(t *testing.T) {
			if err := compiler.bindPageTake(operation, supplied); err != nil {
				t.Fatal(err)
			}
			want := int32(25)
			if name == "explicit" {
				want = 30
			}
			if supplied["take"] != want {
				t.Fatalf("take=%v", supplied["take"])
			}
		})
	}
	for _, value := range []any{int32(0), int32(-1), int32(41), int32(101), "25"} {
		if err := compiler.bindPageTake(operation, map[string]any{"take": value}); err == nil {
			t.Fatalf("accepted take %v", value)
		}
	}
	operation.Pagination.MaxTake = 20
	if err := compiler.bindPageTake(operation, map[string]any{"take": int32(21)}); err == nil {
		t.Fatal("ignored declaration maximum")
	}
}
