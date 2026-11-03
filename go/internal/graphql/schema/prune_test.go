package schema

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func unreachableTypeNames(parsed *ast.Schema) []string {
	seen := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		definition := parsed.Types[name]
		if definition == nil || seen[name] {
			return
		}
		seen[name] = true
		for _, field := range definition.Fields {
			visit(field.Type.Name())
			for _, argument := range field.Arguments {
				visit(argument.Type.Name())
			}
		}
	}
	for _, root := range []*ast.Definition{parsed.Query, parsed.Mutation, parsed.Subscription} {
		if root != nil {
			visit(root.Name)
		}
	}
	var missing []string
	for name, definition := range parsed.Types {
		if !definition.BuiltIn && !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func socialWithOperations(t *testing.T, keep func(model string, current []compilerir.Operation) []compilerir.Operation) compilerir.CompilationIR {
	t.Helper()
	compiled := socialCompilation(t)
	compiled.Contract.Models = append([]compilerir.ModelContractIR(nil), compiled.Contract.Models...)
	for index := range compiled.Contract.Models {
		contract := &compiled.Contract.Models[index]
		contract.Operations = keep(contract.GraphQLName, contract.Operations)
		if !containsOperation(contract.Operations, compilerir.OperationCreate) {
			contract.Roots.Create = ""
		}
	}
	return compiled
}

func socialCompilation(t *testing.T) compilerir.CompilationIR {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	return *compiled.Compilation
}

func unprunedSocialSDL(t *testing.T) string {
	t.Helper()
	sdl, err := assemble(socialCompilation(t))
	if err != nil {
		t.Fatal(err)
	}
	return sdl
}

func containsOperation(values []compilerir.Operation, target compilerir.Operation) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestBuildEmitsOnlyReachableTypes(t *testing.T) {
	withoutPostCreate := socialWithOperations(t, func(model string, current []compilerir.Operation) []compilerir.Operation {
		var kept []compilerir.Operation
		for _, operation := range current {
			if model == "Post" && (operation == compilerir.OperationCreate || operation == compilerir.OperationUpsert) {
				continue
			}
			kept = append(kept, operation)
		}
		return kept
	})
	document, err := Build(withoutPostCreate)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatal(err)
	}
	if unreachable := unreachableTypeNames(parsed); len(unreachable) != 0 {
		t.Fatalf("SDL advertises %d unreachable types: %v", len(unreachable), unreachable)
	}
	for _, absent := range []string{"PostCreateInput", "PostUpsertInput", "PostAnalyticsFilter", "StringAnalyticsFilter", "StringListFilter", "BooleanFilter", "JSONFilter"} {
		if parsed.Types[absent] != nil {
			t.Errorf("unreachable type %s was emitted", absent)
		}
	}
	for _, present := range []string{"UserCreateInput", "PostWhereInput", "StringFilter"} {
		if parsed.Types[present] == nil {
			t.Errorf("reachable type %s was pruned", present)
		}
	}
	if !strings.Contains(document.SDL, "directive @oneOf") {
		t.Fatal("@oneOf is used by reachable inputs but its declaration was pruned")
	}
}

func TestBuildKeepsNestedInputReachableThroughRelations(t *testing.T) {
	onlyUserUpdate := socialWithOperations(t, func(model string, _ []compilerir.Operation) []compilerir.Operation {
		if model == "User" {
			return []compilerir.Operation{compilerir.OperationFindOne, compilerir.OperationUpdate}
		}
		return nil
	})
	document, err := Build(onlyUserUpdate)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatal(err)
	}
	if unreachable := unreachableTypeNames(parsed); len(unreachable) != 0 {
		t.Fatalf("SDL advertises unreachable types: %v", unreachable)
	}
	chain := []string{"UserUpdateInput", "UserPostsUpdateRelationInput", "PostCreateWithoutAuthorInput", "PostCommentsCreateRelationInput", "CommentCreateWithoutPostInput"}
	for _, name := range chain {
		if parsed.Types[name] == nil {
			t.Errorf("nested input %s reachable only through relation inputs was pruned", name)
		}
	}
	for _, absent := range []string{"PostCreateInput", "PostUpdateInput", "CommentCreateInput"} {
		if parsed.Types[absent] != nil {
			t.Errorf("type %s has no path from Mutation.updateUser but was emitted", absent)
		}
	}
}

func TestBuildPruningKeepsDefinitionTextByteIdentical(t *testing.T) {
	document := socialDocument(t)
	original := unprunedSocialSDL(t)
	originalDefinitions := definitionTexts(t, original)
	prunedDefinitions := definitionTexts(t, document.SDL)
	if len(prunedDefinitions) == 0 || len(prunedDefinitions) >= len(originalDefinitions) {
		t.Fatalf("pruned %d definitions of %d", len(prunedDefinitions), len(originalDefinitions))
	}
	for name, text := range prunedDefinitions {
		if originalDefinitions[name] != text {
			t.Errorf("definition %s changed under pruning:\nbefore:\n%s\nafter:\n%s", name, originalDefinitions[name], text)
		}
	}
	again, err := pruneUnreachable(document.SDL)
	if err != nil {
		t.Fatal(err)
	}
	if again != document.SDL {
		t.Fatal("pruning an already pruned document is not a fixed point")
	}
}

func definitionTexts(t *testing.T, sdl string) map[string]string {
	t.Helper()
	result := map[string]string{}
	var name string
	var current []string
	flush := func() {
		if name != "" {
			result[name] = strings.TrimRight(strings.Join(current, "\n"), "\n ")
		}
	}
	for _, line := range strings.Split(sdl, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && !strings.HasPrefix(line, " ") && (fields[0] == "type" || fields[0] == "input" || fields[0] == "enum" || fields[0] == "scalar" || fields[0] == "directive") {
			flush()
			name = fields[0] + " " + strings.TrimSuffix(fields[1], "{")
			current = nil
		}
		current = append(current, line)
	}
	flush()
	return result
}

func TestPruneUnreachableRejectsInvalidInputAndUnsplittableShapes(t *testing.T) {
	valid := "type Query {\n  a: Int\n}\n"
	cases := []struct {
		name string
		sdl  string
		want string
	}{
		{"does not parse", "type Query {", "does not parse"},
		{"undefined field type", "type Query {\n  a: Missing\n}\n", "invalid before pruning"},
		{"duplicate definitions", valid + "\ntype Query {\n  b: Int\n}\n", "invalid before pruning"},
		{"extension", valid + "\nextend type Query {\n  b: Int\n}\n", "schema or extension definitions"},
		{"schema definition", "schema { query: Query }\n" + valid, "schema or extension definitions"},
		{"text before first definition", "# note\n" + valid, "text before its first definition"},
		{"two definitions on one line", "type Query { a: Other } type Other { b: Int }\n", "share line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pruneUnreachable(tc.sdl)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pruneUnreachable error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestPruneUnreachableFollowsArgumentsAndDirectiveArguments(t *testing.T) {
	sdl := strings.Join([]string{
		"directive @tag(kind: Kind!) on INPUT_OBJECT",
		"directive @unused(kind: Gone) on INPUT_OBJECT",
		"enum Kind { a }",
		"enum Gone { a }",
		"input Filter @tag(kind: a) {\n  value: Int\n}",
		"input Orphan {\n  value: Int\n}",
		"type Query {\n  items(where: Filter): Int\n}",
	}, "\n\n") + "\n"
	pruned, err := pruneUnreachable(sdl)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"directive @tag(kind: Kind!) on INPUT_OBJECT",
		"enum Kind { a }",
		"input Filter @tag(kind: a) {\n  value: Int\n}",
		"type Query {\n  items(where: Filter): Int\n}",
	}, "\n\n") + "\n"
	if pruned != want {
		t.Fatalf("pruned =\n%s\nwant\n%s", pruned, want)
	}
}

func TestPruneUnreachableKeepsAllThreeOperationRoots(t *testing.T) {
	sdl := "type Query {\n  q: Int\n}\n\ntype Mutation {\n  m: M\n}\n\ntype M {\n  v: Int\n}\n\ntype Subscription {\n  s: S\n}\n\ntype S {\n  v: Int\n}\n\ntype Dead {\n  v: Int\n}\n"
	pruned, err := pruneUnreachable(sdl)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pruned, "Dead") || !strings.Contains(pruned, "type S {") || !strings.Contains(pruned, "type M {") {
		t.Fatalf("pruned =\n%s", pruned)
	}
}

func TestPruneUnreachableFailsLoudlyOnShapesItDoesNotWalk(t *testing.T) {
	sdl := "type Query {\n  a: Either\n}\n\nunion Either = Left | Right\n\ntype Left {\n  v: Int\n}\n\ntype Right {\n  v: Int\n}\n"
	_, err := pruneUnreachable(sdl)
	if err == nil || !strings.Contains(err.Error(), "pruned GraphQL schema is invalid") {
		t.Fatalf("union members must not be silently dropped: %v", err)
	}
}
