package codegen

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

func emitOperationAdapter(t *testing.T, directory, packageName string, keep func(ir.CustomOperationContractIR) bool) string {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: directory, Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	var operations []ir.CustomOperationContractIR
	for _, operation := range compilation.Contract.CustomOperations {
		if keep(operation) {
			operations = append(operations, operation)
		}
	}
	compilation.Contract.CustomOperations = operations
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Emit(Request{
		PackageName: packageName, AppImportPath: compilation.Model.Schema.PackagePath,
		SDL: document.SDL, ContractFingerprint: compiled.ContractFingerprint, Actor: compilation.Model.Schema.Actor,
		Compilation: &compilation, GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(result.Source)
}

func TestEmitDispatchesCustomMutationsThroughTheOperationEntry(t *testing.T) {
	source := emitOperationAdapter(t, "../../compiler/compile/testdata/graphql_extensions", "graphqlextensions", func(ir.CustomOperationContractIR) bool { return true })
	for _, fragment := range []string{
		"func (caller *golemGeneratedGraphQLCaller[P]) GolemGraphQLDispatchCustomMutation(ctx context.Context, resolver any, run func(context.Context, any) (any, error)) (any, error) {",
		"return golemruntime.DispatchCallerOperation(ctx, caller.public.runtime, resolver, func(ctx context.Context, inner *golemruntime.Caller[P, Actor]) (any, error) {",
		"return run(ctx, golemGeneratedOperationCaller[P](inner))",
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("generated adapter missing %q", fragment)
		}
	}
}

func TestEmitWithoutCustomMutationsHasNoOperationDispatch(t *testing.T) {
	for name, keep := range map[string]func(ir.CustomOperationContractIR) bool{
		"queries only": func(operation ir.CustomOperationContractIR) bool {
			return operation.Operation == ir.CustomOperationQuery
		},
		"none": func(ir.CustomOperationContractIR) bool { return false },
	} {
		source := emitOperationAdapter(t, "../../compiler/compile/testdata/graphql_extensions", "graphqlextensions", keep)
		if strings.Contains(source, "GolemGraphQLDispatchCustomMutation") || strings.Contains(source, "golemGeneratedOperationCaller") {
			t.Errorf("%s: adapter without custom mutations emitted operation dispatch", name)
		}
	}
}
