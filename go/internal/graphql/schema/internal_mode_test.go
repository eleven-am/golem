package schema

import (
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/vektah/gqlparser/v2"
)

func TestInternalModeFieldIsAbsentFromEveryGeneratedGraphQLSurface(t *testing.T) {
	compilation := systemModeCompilation(t, "User", "handle", compilerir.ModeInternal, compilerir.ModeImmutable)
	document, err := Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatalf("invalid SDL: %v", err)
	}
	checked := 0
	for name, definition := range parsed.Types {
		if !strings.HasPrefix(name, "User") {
			continue
		}
		checked++
		if definition.Fields.ForName("handle") != nil {
			t.Errorf("%s still carries handle", name)
		}
		for _, value := range definition.EnumValues {
			if strings.EqualFold(value.Name, "handle") {
				t.Errorf("%s enum still lists %s", name, value.Name)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("only %d User types inspected", checked)
	}
	if def := parsed.Types["UserWhereUniqueInput"]; def != nil && def.Fields.ForName("handle") != nil {
		t.Fatal("whereUnique still selects by handle")
	}
}
