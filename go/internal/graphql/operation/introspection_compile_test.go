package operation

import (
	"strings"
	"testing"

	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestCompilerLeavesQueryMetaRootsToTheExecutable(t *testing.T) {
	compilation := social(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name  string
		query string
		reads []string
	}{
		{"schema", `query Probe { __schema { queryType { name } } }`, nil},
		{"type", `query Probe { __type(name: "Post") { name fields { name } } }`, nil},
		{"typename", `query Probe { __typename }`, nil},
		{"aliased typename", `query Probe { kind: __typename }`, nil},
		{"meta roots through a fragment", `query Probe { ...Meta } fragment Meta on Query { __typename __schema { queryType { name } } }`, nil},
		{"schema beside a read", `query Probe { __schema { queryType { name } } feed: ` + post.Roots.FindMany + `(take: 1) { id } }`, []string{"feed"}},
		{"typename beside a read", `query Probe { __typename feed: ` + post.Roots.FindMany + `(take: 1) { id } }`, []string{"feed"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			query, errors := gqlparser.LoadQuery(schema, testCase.query)
			if len(errors) != 0 {
				t.Fatalf("query errors = %v", errors)
			}
			result, err := compiler.Compile(query, query.Operations.ForName("Probe"), nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if len(result.Reads) != len(testCase.reads) || len(result.Order) != len(testCase.reads) || len(result.Custom) != 0 || len(result.Analytics) != 0 {
				t.Fatalf("compiled roots reads=%d order=%d custom=%d analytics=%d", len(result.Reads), len(result.Order), len(result.Custom), len(result.Analytics))
			}
			for index, name := range testCase.reads {
				if result.Reads[index].ResponseName != name {
					t.Fatalf("read %d = %q, want %q", index, result.Reads[index].ResponseName, name)
				}
			}
		})
	}
}

func TestCompilerLeavesMutationTypenameToTheExecutable(t *testing.T) {
	compilation := social(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	only, errors := gqlparser.LoadQuery(schema, `mutation Probe { __typename }`)
	if len(errors) != 0 {
		t.Fatalf("query errors = %v", errors)
	}
	result, err := compiler.Compile(only, only.Operations.ForName("Probe"), nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(result.Mutations) != 0 || len(result.Custom) != 0 || len(result.Order) != 0 {
		t.Fatalf("compiled roots mutations=%d custom=%d order=%d", len(result.Mutations), len(result.Custom), len(result.Order))
	}
	mixed, errors := gqlparser.LoadQuery(schema, `mutation Probe { __typename bulk: `+post.Roots.UpdateMany+`(where: { all: true }, data: { title: { set: "bulk" } }) { count } }`)
	if len(errors) != 0 {
		t.Fatalf("query errors = %v", errors)
	}
	result, err = compiler.Compile(mixed, mixed.Operations.ForName("Probe"), nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(result.Mutations) != 1 || result.Mutations[0].ResponseName != "bulk" || len(result.Order) != 1 {
		t.Fatalf("compiled mutations=%#v order=%#v", result.Mutations, result.Order)
	}
}

func TestCompilerStillRefusesOperationsThatSelectNoRootFields(t *testing.T) {
	compilation := social(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct{ query, want string }{
		{`query Probe { __typename @skip(if: true) }`, "P5_OPERATION_ROOT: query selects no root fields"},
		{`query Probe { __schema @include(if: false) { queryType { name } } }`, "P5_OPERATION_ROOT: query selects no root fields"},
		{`mutation Probe { __typename @skip(if: true) }`, "P5_OPERATION_ROOT: mutation selects no root fields"},
	} {
		query, errors := gqlparser.LoadQuery(schema, testCase.query)
		if len(errors) != 0 {
			t.Fatalf("query errors = %v", errors)
		}
		_, err := compiler.Compile(query, query.Operations.ForName("Probe"), nil)
		if err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("%s compiled with error %v, want %q", testCase.query, err, testCase.want)
		}
	}
}
