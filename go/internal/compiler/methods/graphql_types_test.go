package methods

import (
	"context"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestInterpretRegisteredGraphQLObjectsAndNestedInputs(t *testing.T) {
	const pkg = "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/objecttypes"
	result := Interpret(context.Background(), Config{Dir: moduleDirectory(t), Compilation: extensionCompilation(pkg), Packages: []modelcodegen.PackageSpec{{ImportPath: pkg, PackageName: "objecttypes", Directory: fixtureDirectory(t, "objecttypes")}}})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
	if len(result.GraphQLTypes) != 5 || len(result.GraphQLCustom) != 1 {
		t.Fatalf("definitions=%#v custom=%#v", result.GraphQLTypes, result.GraphQLCustom)
	}
	if len(result.GraphQLComputed) != 1 || result.GraphQLComputed[0].Field.Result.Name != "HomeFeedPage" || result.GraphQLComputed[0].Field.Result.Nullable || result.GraphQLComputed[0].Field.Arguments[0].Type.Kind != ir.GraphQLTypeInputObject {
		t.Fatalf("computed DTO and nested input=%#v", result.GraphQLComputed)
	}
	definitions := map[string]ir.GraphQLTypeDefinitionIR{}
	for _, definition := range result.GraphQLTypes {
		definitions[definition.Name] = definition
	}
	page := definitions["HomeFeedPage"]
	if page.Go.Name != "HomeFeedPage" || page.Kind != ir.GraphQLTypeObject || page.Fields[0].Name != "results" || page.Fields[0].GoName != "Results" || page.Fields[0].Type.MaxItems != 25 || page.Fields[0].Type.Element.Name != "HomeSegment" {
		t.Fatalf("page=%#v", page)
	}
	field := definitions["HomeInput"].Fields[0]
	if field.Type.Kind != ir.GraphQLTypeInputObject || field.Type.Name != "PageRange" || !field.Type.Nullable {
		t.Fatalf("nested input=%#v", field)
	}
	operation := result.GraphQLCustom[0].Operation
	if operation.Result.Kind != ir.GraphQLTypeObject || operation.Result.Name != "HomeFeedPage" || operation.Arguments[0].Type.Kind != ir.GraphQLTypeInputObject || operation.Arguments[0].Type.Name != "HomeInput" {
		t.Fatalf("operation=%#v", operation)
	}
	if !operation.Arguments[1].PresenceAware || !operation.Arguments[1].Type.Nullable || !result.GraphQLComputed[0].Field.Arguments[1].PresenceAware {
		t.Fatalf("presence-aware arguments were lost: %#v", operation.Arguments)
	}
	if target := definitions["PlaybackTarget"]; !target.OneOf || len(target.Fields) != 5 || !target.Fields[0].PresenceAware || !target.Fields[0].Type.Nullable {
		t.Fatalf("one-of input=%#v", target)
	}
}

func TestInterpretGraphQLObjectsRejectUnsupportedDefinitions(t *testing.T) {
	const pkg = "github.com/eleven-am/golem/go/internal/compiler/methods/testdata/objecttypesinvalid"
	result := Interpret(context.Background(), Config{Dir: moduleDirectory(t), Compilation: extensionCompilation(pkg), Packages: []modelcodegen.PackageSpec{{ImportPath: pkg, PackageName: "objecttypesinvalid", Directory: fixtureDirectory(t, "objecttypesinvalid")}}})
	for _, code := range []string{"P5_OBJECT_TYPE", "P5_OBJECT_FIELD", "P5_OBJECT_FIELD_TYPE", "P5_OBJECT_LIST_MAX", "P5_OBJECT_DEFINITION", "P5_INPUT_OPTION"} {
		if !containsDiagnostic(result.Diagnostics, code) {
			t.Errorf("missing %s in %#v", code, result.Diagnostics)
		}
	}
	for _, name := range []string{"BadPresence.Pointer", "BadPresence.Nested", "BadPresence.Capability"} {
		found := false
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.Code == "P5_OBJECT_FIELD_TYPE" && strings.Contains(diagnostic.Message, name) {
				found = true
			}
		}
		if !found {
			t.Errorf("unsupported input witness %s was accepted", name)
		}
	}
	invalidOutputs := 0
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == "P5_COMPUTED_RESULT_TYPE" {
			invalidOutputs++
		}
	}
	if invalidOutputs != 2 {
		t.Fatalf("unregistered and scalar GraphQLOutput witnesses were not both rejected: %#v", result.Diagnostics)
	}
}
