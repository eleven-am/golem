package schema

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	compilerrowversion "github.com/eleven-am/golem/go/internal/compiler/rowversion"
	gqlparser "github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestRowVersionIsAStringOnSubscribedOutputTypesOnly(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	subscribed := map[string]bool{"Post": true, "Friendship": true}
	for index := range compiled.Compilation.Contract.Models {
		contract := &compiled.Compilation.Contract.Models[index]
		if !subscribed[contract.GraphQLName] {
			continue
		}
		contract.Subscriptions = true
		contract.Roots.Events = contract.GraphQLName + "Events"
		var logical *compilerir.ModelDeclIR
		for modelIndex := range compiled.Compilation.Model.Models {
			if compiled.Compilation.Model.Models[modelIndex].ID == contract.ModelID {
				logical = &compiled.Compilation.Model.Models[modelIndex]
			}
		}
		shape, err := compilerir.BuildEventSchemaShape(*logical, compiled.Compilation.Model.Enums, logical.PrimaryKey.Fields)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := compilerir.EventSchemaFingerprint(shape)
		if err != nil {
			t.Fatal(err)
		}
		identityType := ""
		if len(shape.IdentityFields) > 1 {
			identityType = contract.GraphQLName + "EventIdentity"
		}
		contract.Event = &compilerir.EventContractIR{PayloadTypeName: contract.GraphQLName + "Event", IdentityTypeName: identityType, MetadataFields: []string{"eventID", "type", "id", "entity", "causationID", "transactionOrdinal", "recordedAt"}, Schema: shape, SchemaFingerprint: fingerprint}
	}
	if diagnostics := compilerrowversion.Apply(compiled.Compilation); len(diagnostics) != 0 {
		t.Fatalf("row version diagnostics = %#v", diagnostics)
	}
	document, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatalf("invalid SDL: %v\n%s", err, document.SDL)
	}
	for _, definition := range parsed.Types {
		if definition.BuiltIn {
			continue
		}
		field := definition.Fields.ForName(compilerir.RowVersionGraphQLName)
		switch {
		case definition.Kind == ast.Object && subscribed[definition.Name]:
			if field == nil || field.Type.String() != "String!" {
				t.Fatalf("%s._version = %v, want String!", definition.Name, field)
			}
		case field != nil:
			t.Fatalf("%s %s carries _version; only subscribed output types may", definition.Kind, definition.Name)
		}
	}
	for _, name := range []string{"PostScalarField", "FriendshipScalarField"} {
		for _, value := range requireDefinition(t, parsed, name).EnumValues {
			if value.Name == compilerir.RowVersionGraphQLName {
				t.Fatalf("%s offers _version for distinct", name)
			}
		}
	}
}
