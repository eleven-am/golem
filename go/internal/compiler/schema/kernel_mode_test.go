package schema

import (
	"context"
	"testing"
)

func TestExtractRecordsWithoutGraphQLDeclaration(t *testing.T) {
	result := Extract(context.Background(), Config{Dir: "testdata/kernel", Pattern: "."})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Extract diagnostics: %#v", result.Diagnostics)
	}
	if !result.Raw.Root.WithoutGraphQL {
		t.Fatal("WithoutGraphQL declaration was not recorded")
	}
	social := Extract(context.Background(), Config{Dir: "testdata/social", Pattern: "."})
	if len(social.Diagnostics) != 0 || social.Raw.Root.WithoutGraphQL {
		t.Fatalf("schema without the declaration must keep GraphQL: %#v", social.Diagnostics)
	}
}

func TestExtractRejectsDuplicateAndMalformedWithoutGraphQL(t *testing.T) {
	result := Extract(context.Background(), Config{Dir: "testdata/kernelinvalid", Pattern: "."})
	lines := map[string][]uint32{}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Span.RelativeFile == "" || diagnostic.Span.StartLine == 0 {
			t.Fatalf("diagnostic has no span: %#v", diagnostic)
		}
		lines[diagnostic.Code] = append(lines[diagnostic.Code], diagnostic.Span.StartLine)
	}
	if got := lines["P1_SCHEMA_WITHOUT_GRAPHQL_DUPLICATE"]; len(got) != 1 || got[0] != 17 {
		t.Fatalf("duplicate diagnostics = %v (all %#v)", got, result.Diagnostics)
	}
	if got := lines["P1_SCHEMA_WITHOUT_GRAPHQL_CALL"]; len(got) != 2 || got[0] != 18 || got[1] != 19 {
		t.Fatalf("malformed call diagnostics = %v (all %#v)", got, result.Diagnostics)
	}
	if len(lines["P1_SCHEMA_BODY_CALL"]) != 0 {
		t.Fatalf("WithoutGraphQL must be a registered schema declaration: %#v", result.Diagnostics)
	}
}
