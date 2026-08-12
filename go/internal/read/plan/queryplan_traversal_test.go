package plan

import (
	"reflect"
	"testing"

	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

func TestQueryPlanTraversalCursorExposesOnlyImmediateImmutableFrames(t *testing.T) {
	grandchild := Plan{operation: readir.FindMany}
	child := Plan{operation: readir.FindMany, relations: []Relation{{child: &grandchild}}}
	root := Plan{operation: readir.FindMany, relations: []Relation{{child: &child}}, hydrations: []Relation{{child: &child}}}
	cursor := QueryPlanTraversal(root)
	if len(cursor.Relations()) != 1 || len(cursor.Hydrations()) != 1 {
		t.Fatal("immediate relation classes were not retained")
	}
	if len(cursor.Relations()[0].Child().Relations()) != 1 {
		t.Fatal("child cursor did not advance exactly one frame")
	}
	if len(cursor.Relations()[0].Child().Relations()[0].Child().Relations()) != 0 {
		t.Fatal("grandchild cursor did not terminate")
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(QueryPlanFrame{}), reflect.TypeOf(QueryPlanRelation{})} {
		for index := 0; index < typ.NumField(); index++ {
			if typ.Field(index).IsExported() {
				t.Fatalf("%s exposes field %s", typ, typ.Field(index).Name)
			}
		}
	}
}
