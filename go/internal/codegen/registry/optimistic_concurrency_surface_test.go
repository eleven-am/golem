package registry

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestOptimisticConcurrencyDeclarationAndGeneratedSurfaceAreExact(t *testing.T) {
	request := optimisticConcurrencyRegistryRequest(t, true)
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
	shellSource := string(shell.Source)
	for _, prefix := range []string{"Caller", "System", "CallerTx", "SystemTx"} {
		for _, fragment := range []string{
			"func (client " + prefix + "PostClient[P]) Update(ctx context.Context, target golem.MutationTarget[models.Post], expected golem.ExistingVersion, input models.PostUpdateInput, projection ...golem.Projection[models.Post])",
			"func (client " + prefix + "PostClient[P]) Upsert(ctx context.Context, target golem.MutationTarget[models.Post], expected golem.ConcurrencyExpectation, create models.PostCreateInput, update models.PostUpdateInput, projection ...golem.Projection[models.Post])",
			"func (client " + prefix + "PostClient[P]) Delete(ctx context.Context, target golem.MutationTarget[models.Post], expected golem.ExistingVersion, projection ...golem.Projection[models.Post])",
		} {
			if !strings.Contains(finalSource, fragment) {
				t.Errorf("%s final source missing %q:\n%s", prefix, fragment, finalSource)
			}
		}
		for _, forbidden := range []string{
			"func (client " + prefix + "PostClient[P]) UpdateMany(",
			"func (client " + prefix + "PostClient[P]) DeleteMany(",
		} {
			if strings.Contains(finalSource, forbidden) {
				t.Errorf("%s final source contains forbidden batch method %q:\n%s", prefix, forbidden, finalSource)
			}
		}
	}
	for _, forbidden := range []string{
		"func (CallerPostClient[P]) UpdateMany(", "func (CallerPostClient[P]) DeleteMany(",
		"func (CallerTxPostClient[P]) UpdateMany(", "func (CallerTxPostClient[P]) DeleteMany(",
	} {
		if strings.Contains(shellSource, forbidden) {
			t.Errorf("bootstrap source contains forbidden batch method %q:\n%s", forbidden, shellSource)
		}
	}
	for _, call := range []string{
		"golemruntime.CallerUpdateVersioned", "golemruntime.CallerUpsertVersioned", "golemruntime.CallerDeleteVersioned",
		"golemruntime.SystemUpdateVersioned", "golemruntime.SystemUpsertVersioned", "golemruntime.SystemDeleteVersioned",
		"golemruntime.CallerTxUpdateVersioned", "golemruntime.CallerTxUpsertVersioned", "golemruntime.CallerTxDeleteVersioned",
		"golemruntime.SystemTxUpdateVersioned", "golemruntime.SystemTxUpsertVersioned", "golemruntime.SystemTxDeleteVersioned",
	} {
		if !strings.Contains(finalSource, call) {
			t.Errorf("final registry missing versioned runtime call %q:\n%s", call, finalSource)
		}
	}
	for _, fragment := range []string{
		"func (client CallerUserClient[P]) Update(ctx context.Context, target golem.MutationTarget[models.User], input models.UserUpdateInput, projection ...golem.Projection[models.User])",
		"func (client CallerUserClient[P]) UpdateMany(",
		"func (client CallerUserClient[P]) DeleteMany(",
		"golemruntime.CallerUpdate(ctx, client.runtime",
	} {
		if !strings.Contains(finalSource, fragment) {
			t.Errorf("non-versioned registry surface changed; missing %q:\n%s", fragment, finalSource)
		}
	}

	wantCaller := callerABI(t, final.Source)
	gotCaller := callerABI(t, shell.Source)
	if fmt.Sprint(gotCaller) != fmt.Sprint(wantCaller) {
		t.Fatalf("versioned bootstrap caller ABI differs from final registry\nbootstrap: %v\nfinal:     %v", gotCaller, wantCaller)
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
		"acceptance/optimistic_concurrency_compile_test.go": `package acceptance

import (
	"context"

	"example.test/app/generated"
	"example.test/app/models"
	golem "github.com/eleven-am/golem/go/golem"
)

func caller(ctx context.Context, client generated.CallerPostClient[string], target golem.MutationTarget[models.Post], create models.PostCreateInput, update models.PostUpdateInput) {
	_, _ = client.Update(ctx, target, golem.ExpectVersion(1), update)
	_, _ = client.Upsert(ctx, target, golem.ExpectExisting(1), create, update)
	_, _ = client.Delete(ctx, target, golem.ExpectVersion(1))
}

func system(ctx context.Context, client generated.SystemPostClient[string], target golem.MutationTarget[models.Post], create models.PostCreateInput, update models.PostUpdateInput) {
	_, _ = client.Update(ctx, target, golem.ExpectVersion(1), update)
	_, _ = client.Upsert(ctx, target, golem.ExpectAbsent(), create, update)
	_, _ = client.Delete(ctx, target, golem.ExpectVersion(1))
}

func callerTx(ctx context.Context, client generated.CallerTxPostClient[string], target golem.MutationTarget[models.Post], create models.PostCreateInput, update models.PostUpdateInput) {
	_, _ = client.Update(ctx, target, golem.ExpectVersion(1), update)
	_, _ = client.Upsert(ctx, target, golem.ExpectExisting(1), create, update)
	_, _ = client.Delete(ctx, target, golem.ExpectVersion(1))
}

func systemTx(ctx context.Context, client generated.SystemTxPostClient[string], target golem.MutationTarget[models.Post], create models.PostCreateInput, update models.PostUpdateInput) {
	_, _ = client.Update(ctx, target, golem.ExpectVersion(1), update)
	_, _ = client.Upsert(ctx, target, golem.ExpectAbsent(), create, update)
	_, _ = client.Delete(ctx, target, golem.ExpectVersion(1))
}

func nonVersioned(ctx context.Context, client generated.CallerUserClient[string], target golem.MutationTarget[models.User], create models.UserCreateInput, update models.UserUpdateInput, updateMany models.UserUpdateManyInput) {
	_, _ = client.Update(ctx, target, update)
	_, _ = client.Upsert(ctx, target, create, update)
	_, _ = client.Delete(ctx, target)
	_, _ = client.UpdateMany(ctx, golem.All[models.User](), updateMany)
	_, _ = client.DeleteMany(ctx, golem.All[models.User]())
}
`,
	}, "generated/"+Filename, final.Source)
}

func TestNonVersionedRegistryEmissionBytesRemainFrozenAcrossConcurrencyBranch(t *testing.T) {
	result, err := Emit(optimisticConcurrencyRegistryRequest(t, false))
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(result.Source))
	const want = "74655f1dc33681f769836c64c1d9e915b0599461364af89c119f69dfc1e454c0"
	if digest != want {
		t.Fatalf("non-versioned registry source digest=%s; want %s", digest, want)
	}
}

func optimisticConcurrencyRegistryRequest(t *testing.T, versioned bool) Request {
	t.Helper()
	actor := ir.GoNamedTypeIR{PackagePath: "example.test/app/security", Name: "Actor"}
	postID := ir.ModelID("00000000000000000000000000000001")
	userID := ir.ModelID("00000000000000000000000000000002")
	versionID := ir.FieldID("00000000000000000000000000000013")
	model := ir.ModelIR{
		FormatVersion: ir.ModelFormatVersion,
		Schema: ir.SchemaIdentityIR{
			ID: "example.test/schema", StableName: "test", PackagePath: "example.test/app", RootFunction: "DefineSchema", Actor: actor,
		},
		Models: []ir.ModelDeclIR{
			{ID: postID, CanonicalIdentity: "example.test/Post", Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/models", Name: "Post"}, LogicalName: "Post", Fields: []ir.FieldIR{
				{ID: "00000000000000000000000000000011", CanonicalIdentity: "example.test/Post.ID", GoName: "ID", LogicalName: "id", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}},
				{ID: "00000000000000000000000000000012", CanonicalIdentity: "example.test/Post.Title", GoName: "Title", LogicalName: "title", DeclarationOrder: 1, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeString}}},
				{ID: versionID, CanonicalIdentity: "example.test/Post.Version", GoName: "Version", LogicalName: "version", DeclarationOrder: 2, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}},
			}},
			{ID: userID, CanonicalIdentity: "example.test/User", Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/models", Name: "User"}, LogicalName: "User"},
		},
	}
	contract := ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{
		{ModelID: postID, Fields: []ir.FieldContractIR{{FieldID: versionID, Modes: []ir.FieldMode{ir.ModeVisible}}}},
		{ModelID: userID},
	}}
	if versioned {
		model.Models[0].OptimisticConcurrency = &versionID
		contract.Models[0].OptimisticConcurrency = &versionID
	}
	modelFingerprint, err := ir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := ir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		AppPackage:         modelcodegen.PackageSpec{ImportPath: "example.test/app/generated", PackageName: "generated"},
		ModelPackages:      []modelcodegen.PackageSpec{{ImportPath: "example.test/app/models", PackageName: "models"}},
		Actor:              actor,
		GenerationDigest:   strings.Repeat("0", 64),
		GeneratorVersion:   "test-generator",
		TemplateABIVersion: "test-template",
		Schema:             SchemaInput{Model: model, Contract: contract, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint},
	}
}
