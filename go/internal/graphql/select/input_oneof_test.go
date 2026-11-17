package selectset

import (
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"testing"
)

func TestRegisteredOneOfInputRequiresOneNonNullKnownMember(t *testing.T) {
	typ := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "PlaybackTarget"}
	definition := compilerir.GraphQLTypeDefinitionIR{Kind: typ.Kind, Name: typ.Name, OneOf: true}
	for _, name := range []string{"videoId", "mediaId", "playlistId", "playlistVideoId", "playQueueItemId"} {
		definition.Fields = append(definition.Fields, compilerir.GraphQLObjectFieldIR{Name: name, Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String", Nullable: true}, PresenceAware: true})
	}
	compilation := compilerir.CompilationIR{Contract: compilerir.ContractIR{GraphQLTypes: []compilerir.GraphQLTypeDefinitionIR{definition}}}
	for _, input := range []map[string]any{{"videoId": "v"}, {"mediaId": ""}, {"playQueueItemId": "q"}} {
		if _, err := CoerceExtensionValue(compilation, typ, input, 10); err != nil {
			t.Fatalf("valid choice=%#v: %v", input, err)
		}
	}
	for _, input := range []map[string]any{{}, {"videoId": nil}, {"videoId": "v", "mediaId": "m"}, {"unknown": "u"}, {"videoId": "v", "unknown": nil}} {
		if _, err := CoerceExtensionValue(compilation, typ, input, 10); err == nil {
			t.Errorf("accepted invalid one-of %#v", input)
		}
	}
}
