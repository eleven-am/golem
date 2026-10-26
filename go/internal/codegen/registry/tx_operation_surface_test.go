package registry

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const operationTxExtension = "a0000000000000000000000000000003"

func txOperationRequest(t *testing.T, mutations []ir.CustomOperationContractIR, operations ...ir.TxOperationContractIR) Request {
	t.Helper()
	request := operationRequest(t, mutations...)
	request.Schema.Contract.TxOperations = operations
	fingerprint, err := ir.ContractFingerprint(request.Schema.Contract)
	if err != nil {
		t.Fatal(err)
	}
	request.Schema.ContractFingerprint = fingerprint
	return request
}

func txOperation(extension, name, function string) ir.TxOperationContractIR {
	return ir.TxOperationContractIR{
		ExtensionID: ir.ExtensionID(extension), Name: name,
		Operation: ir.AttachedMethodIR{PackagePath: "example.test/app", Name: function, Kind: "txoperation"},
	}
}

func TestRegistryRegistersTxOperationsAndEmitsRunBesideMutate(t *testing.T) {
	file, err := Emit(txOperationRequest(t, []ir.CustomOperationContractIR{operationMutation(operationMutationExtension, "createInvite", "CreateInvite")}, txOperation(operationTxExtension, "recordInvite", "RecordInvite")))
	if err != nil {
		t.Fatal(err)
	}
	source := string(file.Source)
	for _, fragment := range []string{
		"return golem.GeneratedApplicationOperations(bindings,",
		`golem.GeneratedCustomMutationOperation("` + operationMutationExtension + `", CreateInvite),`,
		`golem.GeneratedTxOperation("` + operationTxExtension + `", RecordInvite),`,
		"func Mutate[P, Args, R any](",
		"func Run[P, Args, R any](ctx context.Context, tx *CallerTx[P], operation func(context.Context, *CallerTx[P], Args) (R, error), arguments Args) (R, error) {",
		"return golemruntime.RunCallerTxOperation(ctx, inner, operation, golemGeneratedOperationTx[P], arguments)",
		"func golemGeneratedOperationTx[P any](inner *golemruntime.CallerTx[P, Actor]) *CallerTx[P] {",
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("generated registry missing %q:\n%s", fragment, source)
		}
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
func RecordInvite(context.Context, *CallerTx[Principal], InviteArgs) (bool, error) { return true, nil }
`,
		"app/run_test.go": `package app
import (
	"context"
	"strings"
	"testing"
)
func TestRunInfersTheOperationSignatureAndRefusesWithoutATransaction(t *testing.T) {
	recorded, err := Run(context.Background(), nil, RecordInvite, InviteArgs{})
	if recorded || err == nil || !strings.Contains(err.Error(), "P5_CUSTOM_OPERATION") {
		t.Fatalf("Run without a transaction = %t, %v", recorded, err)
	}
}
`,
	}, "app/"+Filename, file.Source)
}

func TestRegistryWithOnlyTxOperationsRegistersThemWithoutMutate(t *testing.T) {
	file, err := Emit(txOperationRequest(t, nil, txOperation(operationTxExtension, "recordInvite", "RecordInvite")))
	if err != nil {
		t.Fatal(err)
	}
	source := string(file.Source)
	for _, fragment := range []string{"return golem.GeneratedApplicationOperations(bindings,", `golem.GeneratedTxOperation("` + operationTxExtension + `", RecordInvite),`, "func Run[P, Args, R any]("} {
		if !strings.Contains(source, fragment) {
			t.Errorf("generated registry missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"func Mutate[", "GeneratedCustomMutationOperation", "golemGeneratedOperationCaller"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("registry without custom mutations emitted %q", forbidden)
		}
	}
}

func TestRegistryWithoutTxOperationsEmitsNoRun(t *testing.T) {
	for name, request := range map[string]Request{
		"none":          operationRequest(t),
		"mutation only": operationRequest(t, operationMutation(operationMutationExtension, "createInvite", "CreateInvite")),
	} {
		file, err := Emit(request)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"GeneratedTxOperation", "func Run[", "golemGeneratedOperationTx", "RunCallerTxOperation"} {
			if strings.Contains(string(file.Source), forbidden) {
				t.Errorf("%s: registry without transaction operations emitted %q", name, forbidden)
			}
		}
	}
}

func TestRegistrySurfaceDeclaresRunWithoutAnOperationsTable(t *testing.T) {
	request := txOperationRequest(t, nil, txOperation(operationTxExtension, "recordInvite", "RecordInvite"))
	file, err := EmitSurface(SurfaceRequest{AppPackage: request.AppPackage, ModelPackages: request.ModelPackages, Actor: request.Actor, Model: request.Schema.Model, Contract: request.Schema.Contract})
	if err != nil {
		t.Fatal(err)
	}
	source := string(file.Source)
	if !strings.Contains(source, "func Run[P, Args, R any](") || !strings.Contains(source, "func golemGeneratedOperationTx[P any](") {
		t.Fatalf("surface lacks the published Run declaration:\n%s", source)
	}
	if strings.Contains(source, "GeneratedTxOperation") {
		t.Fatal("surface stub registered operations")
	}
}
