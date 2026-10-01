package registry

import (
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	operationMutationExtension = "a0000000000000000000000000000001"
	operationQueryExtension    = "a0000000000000000000000000000002"
)

func operationRequest(t *testing.T, operations ...ir.CustomOperationContractIR) Request {
	t.Helper()
	spec := modelcodegen.PackageSpec{ImportPath: "example.test/app", PackageName: "app"}
	request := completeRequest(t, Request{AppPackage: spec, ModelPackages: []modelcodegen.PackageSpec{spec}, Actor: ir.GoNamedTypeIR{PackagePath: spec.ImportPath, Name: "Actor"}, GenerationDigest: strings.Repeat("c", 64)})
	request.Schema.Contract.CustomOperations = operations
	fingerprint, err := ir.ContractFingerprint(request.Schema.Contract)
	if err != nil {
		t.Fatal(err)
	}
	request.Schema.ContractFingerprint = fingerprint
	return request
}

func operationMutation(extension, name, resolver string) ir.CustomOperationContractIR {
	return ir.CustomOperationContractIR{
		ExtensionID: ir.ExtensionID(extension), Operation: ir.CustomOperationMutation, Name: name,
		Arguments: []ir.GraphQLArgumentContractIR{}, Result: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Boolean"},
		Resolver:   ir.AttachedMethodIR{PackagePath: "example.test/app", Name: resolver, Kind: "custommutation"},
		Capability: ir.CustomOperationCallerOnly,
	}
}

func operationQuery(extension, name, resolver string) ir.CustomOperationContractIR {
	operation := operationMutation(extension, name, resolver)
	operation.Operation = ir.CustomOperationQuery
	operation.Resolver.Kind = "customquery"
	return operation
}

func TestRegistryRegistersEveryCustomMutationAndOnlyMutations(t *testing.T) {
	file, err := Emit(operationRequest(t, operationQuery(operationQueryExtension, "searchInvites", "SearchInvites"), operationMutation(operationMutationExtension, "createInvite", "CreateInvite")))
	if err != nil {
		t.Fatal(err)
	}
	source := string(file.Source)
	for _, fragment := range []string{
		"bindings, err := golem.GeneratedApplicationBindings(golemGeneratedGenerationDigest(),",
		"return golem.GeneratedApplicationOperations(bindings,",
		`golem.GeneratedCustomMutationOperation("` + operationMutationExtension + `", CreateInvite),`,
		"func Mutate[P, Args, R any](ctx context.Context, caller *Caller[P], resolver func(context.Context, *Caller[P], Args) (R, error), arguments Args) (R, error) {",
		"return golemruntime.RunCallerOperation(ctx, inner, resolver, golemGeneratedOperationCaller[P], arguments)",
		"func golemGeneratedOperationCaller[P any](inner *golemruntime.Caller[P, Actor]) *Caller[P] {",
		"// Mutate runs resolver, a custom mutation declared with golem.Mutation,",
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("generated registry missing %q:\n%s", fragment, source)
		}
	}
	if strings.Contains(source, `GeneratedCustomMutationOperation("`+operationQueryExtension) || strings.Contains(source, "SearchInvites)") {
		t.Error("a custom query resolver was registered as an operation")
	}
	compileRegistry(t, map[string]string{
		"app/app.go": `package app
import (
	"context"
	golem "github.com/eleven-am/golem/go/golem"
)
type Actor struct{}
type Principal struct{}
type InviteArgs struct{}
func GolemGeneratedBindings() golem.PackageBindings[Actor] {
	return golem.GeneratedPackageBindings[Actor](nil, nil)
}
func GolemGeneratedDescriptors() golem.PackageDescriptors { return golem.GeneratedPackageDescriptors() }
func CreateInvite(context.Context, *Caller[Principal], InviteArgs) (bool, error) { return true, nil }
func SearchInvites(context.Context, *Caller[Principal], InviteArgs) (bool, error) { return true, nil }
`,
		"app/mutate_test.go": `package app
import (
	"context"
	"strings"
	"testing"
)
func TestMutateInfersTheResolverSignatureAndRefusesWithoutACaller(t *testing.T) {
	created, err := Mutate(context.Background(), nil, CreateInvite, InviteArgs{})
	if created || err == nil || !strings.Contains(err.Error(), "P5_CUSTOM_OPERATION") {
		t.Fatalf("Mutate without a caller = %t, %v", created, err)
	}
}
`,
	}, "app/"+Filename, file.Source)
}

func TestRegistryWithoutCustomMutationsEmitsNoOperationSurface(t *testing.T) {
	for name, request := range map[string]Request{
		"none":       operationRequest(t),
		"query only": operationRequest(t, operationQuery(operationQueryExtension, "searchInvites", "SearchInvites")),
	} {
		file, err := Emit(request)
		if err != nil {
			t.Fatal(err)
		}
		source := string(file.Source)
		for _, forbidden := range []string{"GeneratedApplicationOperations", "GeneratedCustomMutationOperation", "func Mutate[", "golemGeneratedOperationCaller", "RunCallerOperation"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s: registry without custom mutations emitted %q", name, forbidden)
			}
		}
		if !strings.Contains(source, "\treturn golem.GeneratedApplicationBindings(golemGeneratedGenerationDigest(),") {
			t.Errorf("%s: application bindings no longer return directly", name)
		}
	}
}

func TestRegistrySurfaceDeclaresMutateWithoutAnOperationsTable(t *testing.T) {
	request := operationRequest(t, operationMutation(operationMutationExtension, "createInvite", "CreateInvite"))
	file, err := EmitSurface(SurfaceRequest{AppPackage: request.AppPackage, ModelPackages: request.ModelPackages, Actor: request.Actor, Model: request.Schema.Model, Contract: request.Schema.Contract})
	if err != nil {
		t.Fatal(err)
	}
	source := string(file.Source)
	if !strings.Contains(source, "func Mutate[P, Args, R any](") || !strings.Contains(source, "func golemGeneratedOperationCaller[P any](") {
		t.Fatalf("surface lacks the published Mutate declaration:\n%s", source)
	}
	if strings.Contains(source, "GeneratedCustomMutationOperation") {
		t.Fatal("surface stub registered operations")
	}
}
