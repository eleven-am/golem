package registry

import (
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestGeneratedTransactionsAcceptTransactionOptions(t *testing.T) {
	request := optimisticConcurrencyRegistryRequest(t, false)
	final, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	shell, err := EmitShell(ShellRequest{
		AppPackage:      request.AppPackage,
		Actor:           request.Actor,
		Model:           request.Schema.Model,
		GolemImportPath: request.GolemImportPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalSource := string(final.Source)
	for _, fragment := range []string{
		"func (caller *Caller[P]) Transaction(ctx context.Context, callback func(*CallerTx[P]) error, options ...golem.TransactionOption) error {",
		"golemruntime.CallerTransaction(ctx, (*golemruntime.Caller[P, actorpkg.Actor])(nil), nil, options...)",
		"golemruntime.CallerTransaction(ctx, caller.runtime, nil, options...)",
		"func (system System[P]) Transaction(ctx context.Context, callback func(*SystemTx[P]) error, options ...golem.TransactionOption) error {",
		"golemruntime.SystemTransaction(ctx, system.runtime, nil, options...)",
	} {
		if !strings.Contains(finalSource, fragment) {
			t.Errorf("final registry missing %q:\n%s", fragment, finalSource)
		}
	}
	if got := strings.Count(finalSource, "\t\treturn callback(result)\n\t}, options...)\n}\n"); got != 2 {
		t.Errorf("final registry forwards options from %d transaction callbacks; want 2", got)
	}
	if fragment := "func (caller *Caller[P]) Transaction(_ context.Context, _ func(*CallerTx[P]) error, _ ...golem.TransactionOption) error {"; !strings.Contains(string(shell.Source), fragment) {
		t.Errorf("bootstrap registry missing %q:\n%s", fragment, shell.Source)
	}
	models, err := modelcodegen.Emit(modelcodegen.Request{
		Compilation: ir.CompilationIR{Model: request.Schema.Model, Contract: request.Schema.Contract},
		Packages:    request.ModelPackages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Files) != 1 {
		t.Fatalf("model files=%d; want 1", len(models.Files))
	}
	compileRegistry(t, map[string]string{
		"security/actor.go": "package security\n\ntype Actor struct{}\n",
		"models/models.go":  "package models\n\ntype Post struct{}\ntype User struct{}\n",
		"models/bindings.go": `package models

import (
	"example.test/app/security"
	golem "github.com/eleven-am/golem/go/golem"
)

func GolemGeneratedBindings() golem.PackageBindings[security.Actor] {
	return golem.GeneratedPackageBindings[security.Actor](nil, nil)
}
`,
		"models/" + modelcodegen.BootstrapFilename: string(models.Files[0].Source),
		"acceptance/transaction_option_compile_test.go": `package acceptance

import (
	"context"

	"example.test/app/generated"
	golem "github.com/eleven-am/golem/go/golem"
)

func transactions(ctx context.Context, caller *generated.Caller[string], system generated.System[string]) {
	_ = caller.Transaction(ctx, func(*generated.CallerTx[string]) error { return nil })
	_ = caller.Transaction(ctx, func(*generated.CallerTx[string]) error { return nil }, golem.Isolation(golem.Serializable), golem.RetryConflicts(3))
	_ = system.Transaction(ctx, func(*generated.SystemTx[string]) error { return nil })
	_ = system.Transaction(ctx, func(*generated.SystemTx[string]) error { return nil }, golem.Isolation(golem.ReadCommitted))
}
`,
	}, "generated/"+Filename, final.Source)
}
