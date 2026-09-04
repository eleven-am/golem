package registry

import (
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func declarationDiscoveryShellFiles(t *testing.T, artifacts readSurfaceArtifacts) map[string]string {
	t.Helper()
	shell, err := EmitShell(ShellRequest{
		AppPackage:           modelcodegen.PackageSpec{ImportPath: "example.test/app/schema", PackageName: "schema", Directory: "schema"},
		Actor:                ir.GoNamedTypeIR{PackagePath: "example.test/app/security", Name: "Actor"},
		Model:                artifacts.model,
		Contract:             artifacts.contract,
		DeclarationDiscovery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	files := cloneSourceFiles(artifacts.files)
	files["schema/"+Filename] = string(shell.Source)
	return files
}

func TestDeclarationDiscoveryShellTypeChecksAnExtensionBodyCallingSystemEscape(t *testing.T) {
	artifacts := buildReadSurfaceArtifacts(t)
	files := declarationDiscoveryShellFiles(t, artifacts)
	files["schema/extensions.go"] = `package schema

import (
	"context"

	"example.test/app/models"
)

type RenameArgs struct{ Handle string }

func RenameUser(ctx context.Context, caller *Caller[string], args RenameArgs) (int64, error) {
	var affected int64
	err := caller.Transaction(ctx, func(tx *CallerTx[string]) error {
		if _, err := tx.Users.Count(ctx); err != nil {
			return err
		}
		system := SystemEscape(tx)
		if _, err := system.Users.Count(ctx); err != nil {
			return err
		}
		count, err := system.Users.UpdateMany(ctx,
			models.Users.Name.Eq(args.Handle),
			models.Users.UpdateMany(models.Users.Name.Set("system-owned")),
		)
		affected = count
		return err
	})
	return affected, err
}
`
	runFreshReadSurfaceModule(t, files, false, nil)
}

func TestPolicyDeclarationCannotObtainACallerTransaction(t *testing.T) {
	artifacts := buildReadSurfaceArtifacts(t)
	rejected := declarationDiscoveryShellFiles(t, artifacts)
	rejected["schema/policy.go"] = `package schema

import (
	"example.test/app/models"
	"example.test/app/security"
	"github.com/eleven-am/golem/go/golem"
)

func definePolicy(rules *golem.Rules[models.User], actor security.Actor) {
	_ = SystemEscape[string](rules)
	_ = SystemEscape[string](actor)
}
`
	runFreshReadSurfaceModule(t, rejected, true, []string{
		"cannot use rules (variable of type *golem.Rules[models.User]) as *CallerTx[string] value in argument to SystemEscape[string]",
		"cannot use actor (variable of struct type security.Actor) as *CallerTx[string] value in argument to SystemEscape[string]",
	})

	inert := cloneSourceFiles(artifacts.files)
	inert["generated/"+Filename] = string(artifacts.registry)
	inert["acceptance/fabricated_transaction_test.go"] = `package acceptance_test

import (
	"context"
	"strings"
	"testing"

	"example.test/app/generated"
	"example.test/app/models"
	"github.com/eleven-am/golem/go/golem"
)

func TestFabricatedCallerTransactionEscapesToNothing(t *testing.T) {
	ctx := context.Background()
	system := generated.SystemEscape(&generated.CallerTx[string]{})
	if _, err := system.Users.Count(ctx); err == nil || !strings.Contains(err.Error(), "system transaction is unavailable") {
		t.Fatalf("fabricated escape read err=%v", err)
	}
	input := models.Users.Create(models.Users.ID.Create(golem.NewUUID([16]byte{9})), models.Users.Name.Create("forged"))
	if _, err := system.Users.Create(ctx, input); err == nil || !strings.Contains(err.Error(), "system transaction is unavailable") {
		t.Fatalf("fabricated escape write err=%v", err)
	}
}
`
	runFreshReadSurfaceModule(t, inert, false, nil)
}
