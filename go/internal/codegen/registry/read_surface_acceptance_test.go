package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

const readSurfaceGenerationDigest = "0000000000000000000000000000000000000000000000000000000000000001"

type readSurfaceArtifacts struct {
	files          map[string]string
	registry       []byte
	sqliteManifest migration.Manifest
	sqliteFiles    map[string][]byte
}

func TestGeneratedMutationSurfaceEmitsEveryCallerAndSystemMethod(t *testing.T) {
	source := string(buildReadSurfaceArtifacts(t).registry)
	for _, fragment := range []string{
		"func (client CallerUserClient[P]) Create(",
		"func (client CallerUserClient[P]) Update(",
		"func (client CallerUserClient[P]) Upsert(",
		"func (client CallerUserClient[P]) Delete(",
		"func (client CallerUserClient[P]) UpdateMany(",
		"func (client CallerUserClient[P]) DeleteMany(",
		"func (client SystemUserClient[P]) Create(",
		"func (client SystemUserClient[P]) Update(",
		"func (client SystemUserClient[P]) Upsert(",
		"func (client SystemUserClient[P]) Delete(",
		"func (client SystemUserClient[P]) UpdateMany(",
		"func (client SystemUserClient[P]) DeleteMany(",
		"func (client CallerTxUserClient[P]) Create(",
		"func (client CallerTxUserClient[P]) Update(",
		"func (client CallerTxUserClient[P]) Upsert(",
		"func (client CallerTxUserClient[P]) Delete(",
		"func (client CallerTxUserClient[P]) UpdateMany(",
		"func (client CallerTxUserClient[P]) DeleteMany(",
		"func (client SystemTxUserClient[P]) Create(",
		"func (client SystemTxUserClient[P]) Update(",
		"func (client SystemTxUserClient[P]) Upsert(",
		"func (client SystemTxUserClient[P]) Delete(",
		"func (client SystemTxUserClient[P]) UpdateMany(",
		"func (client SystemTxUserClient[P]) DeleteMany(",
		"golemruntime.CallerCreate", "golemruntime.SystemUpdate", "golemruntime.CallerUpsert", "golemruntime.SystemTxUpsert",
		"golemruntime.CallerTxDelete", "golemruntime.SystemTxCreate",
		"golemruntime.CallerUpdateMany", "golemruntime.SystemDeleteMany",
		"golemruntime.CallerTxDeleteMany", "golemruntime.SystemTxUpdateMany",
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("generated registry missing %q:\n%s", fragment, source)
		}
	}
}

func TestGeneratedMutationSurfaceAcceptsLegalPrograms(t *testing.T) {
	artifacts := buildReadSurfaceArtifacts(t)
	files := cloneSourceFiles(artifacts.files)
	files["generated/"+Filename] = string(artifacts.registry)
	files["acceptance/legal_mutation_surface_test.go"] = `package acceptance_test

import (
	"example.test/app/models"
	"github.com/eleven-am/golem/go/golem"
)

var legalUserID = golem.NewUUID([16]byte{1})
var legalPostID = golem.NewUUID([16]byte{2})
var legalUserTarget = models.Users.ByID.Value(legalUserID)
var legalPostTarget = models.Posts.ByID.Value(legalPostID)
var legalUserCreate = models.Users.Create(models.Users.ID.Create(legalUserID), models.Users.Name.Create("user"))
var legalUserUpdate = models.Users.Update(models.Users.Name.Set("updated-user"))
var legalPostCreate = models.Posts.Create(models.Posts.ID.Create(legalPostID), models.Posts.AuthorID.Create(legalUserID), models.Posts.Title.Create("post"))
var legalPostUpdate = models.Posts.Update(models.Posts.Title.Set("updated-post"))
var legalPostUpdateMany = models.Posts.UpdateMany(models.Posts.Title.Set("many"))

var _ models.PostCreateInput = models.Posts.Create(
	models.Posts.Author.Create(legalUserCreate),
	models.Posts.Author.Connect(legalUserTarget),
	models.Posts.Author.ConnectOrCreate(legalUserTarget, legalUserCreate),
)
var _ models.PostUpdateInput = models.Posts.Update(
	models.Posts.Author.Update(legalUserUpdate),
	models.Posts.Author.Upsert(legalUserCreate, legalUserUpdate),
)
var _ models.UserCreateInput = models.Users.Create(
	models.Users.Posts.Create(legalPostCreate),
	models.Users.Posts.CreateMany(legalPostCreate),
	models.Users.Posts.Connect(legalPostTarget),
	models.Users.Posts.ConnectOrCreate(legalPostTarget, legalPostCreate),
)
var _ models.UserUpdateInput = models.Users.Update(
	models.Users.Posts.Disconnect(legalPostTarget),
	models.Users.Posts.Set(legalPostTarget),
	models.Users.Posts.Update(legalPostTarget, legalPostUpdate),
	models.Users.Posts.UpdateMany(models.Posts.Title.Eq("post"), legalPostUpdateMany),
	models.Users.Posts.Upsert(legalPostTarget, legalPostCreate, legalPostUpdate),
	models.Users.Posts.Delete(legalPostTarget),
	models.Users.Posts.DeleteMany(models.Posts.Title.Eq("post")),
)
`
	runFreshReadSurfaceModule(t, files, false, nil)
}

func TestGeneratedMutationAndTransactionSurfacesExecuteFromFreshModule(t *testing.T) {
	artifacts := buildReadSurfaceArtifacts(t)
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "generated-read-surface.db")
	database, _, err := sqliteprovider.New().Open(ctx, "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.New().ApplyMigration(ctx, database, artifacts.sqliteManifest, artifacts.sqliteFiles); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	for _, row := range [][2]string{
		{"00000000-0000-0000-0000-000000000001", "alice"},
		{"00000000-0000-0000-0000-000000000002", "bob"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	files := cloneSourceFiles(artifacts.files)
	files["generated/"+Filename] = string(artifacts.registry)
	files["acceptance/read_surface_test.go"] = fmt.Sprintf(`package acceptance_test

import (
	"context"
	"fmt"
	"testing"

	"example.test/app/generated"
	"example.test/app/models"
	"example.test/app/security"
	"github.com/eleven-am/golem/go/golem"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

func TestEveryGeneratedReadOperation(t *testing.T) {
	ctx := context.Background()
	database, err := providersqlite.Open(ctx, providersqlite.Config{DataSourceName: %q})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = database.Close() })
	application, err := generated.Open(ctx, generated.Config[string]{
		Database: database,
		ReadLimits: golemruntime.ReadLimits{MaxTake: 2},
		ResolvePrincipal: func(context.Context, string) (security.Actor, error) {
			return security.Actor{Prefix: "a"}, nil
		},
	})
	if err != nil { t.Fatal(err) }
	aliceID, err := golem.ParseUUID("00000000-0000-0000-0000-000000000001")
	if err != nil { t.Fatal(err) }
	bobID, err := golem.ParseUUID("00000000-0000-0000-0000-000000000002")
	if err != nil { t.Fatal(err) }
	alice := models.Users.ByID.Value(aliceID)
	bob := models.Users.ByID.Value(bobID)
	projection := models.Users.Select(models.Users.ID, models.Users.Name)
	order := models.Users.OrderBy(models.Users.Name.Asc())

	caller, err := application.ForPrincipal(ctx, "principal")
	if err != nil { t.Fatal(err) }
	callerRows, err := caller.Users.FindMany(ctx, order, projection)
	if err != nil || len(callerRows) != 1 { t.Fatalf("caller findMany rows=%%d err=%%v", len(callerRows), err) }
	callerFirst, found, err := caller.Users.FindFirst(ctx, order, projection)
	if err != nil || !found { t.Fatalf("caller findFirst found=%%t err=%%v", found, err) }
	if name, ok := golem.Value(callerFirst, models.Users.Name).Get(); !ok || name != "alice" { t.Fatalf("caller first=%%q present=%%t", name, ok) }
	callerUnique, err := caller.Users.FindUnique(ctx, alice, projection)
	if err != nil { t.Fatal(err) }
	if name, ok := golem.Value(callerUnique, models.Users.Name).Get(); !ok || name != "alice" { t.Fatalf("caller unique=%%q present=%%t", name, ok) }
	callerCount, err := caller.Users.Count(ctx)
	if err != nil || callerCount != 1 { t.Fatalf("caller count=%%d err=%%v", callerCount, err) }
	if err := caller.Transaction(ctx, func(tx *generated.CallerTx[string]) error {
		count, err := tx.Users.Count(ctx)
		if err != nil || count != 1 { return fmt.Errorf("caller tx count=%%d err=%%v", count, err) }
		_, err = tx.Users.FindUnique(ctx, alice, projection)
		return err
	}); err != nil { t.Fatal(err) }

	system := application.System()
	systemRows, err := system.Users.FindMany(ctx, order, projection)
	if err != nil || len(systemRows) != 2 { t.Fatalf("system findMany rows=%%d err=%%v", len(systemRows), err) }
	systemFirst, found, err := system.Users.FindFirst(ctx, order, projection)
	if err != nil || !found { t.Fatalf("system findFirst found=%%t err=%%v", found, err) }
	if name, ok := golem.Value(systemFirst, models.Users.Name).Get(); !ok || name != "alice" { t.Fatalf("system first=%%q present=%%t", name, ok) }
	systemUnique, err := system.Users.FindUnique(ctx, bob, projection)
	if err != nil { t.Fatal(err) }
	if name, ok := golem.Value(systemUnique, models.Users.Name).Get(); !ok || name != "bob" { t.Fatalf("system unique=%%q present=%%t", name, ok) }
	systemCount, err := system.Users.Count(ctx)
	if err != nil || systemCount != 2 { t.Fatalf("system count=%%d err=%%v", systemCount, err) }
	if err := system.Transaction(ctx, func(tx *generated.SystemTx[string]) error {
		count, err := tx.Users.Count(ctx)
		if err != nil || count != 2 { return fmt.Errorf("system tx count=%%d err=%%v", count, err) }
		_, err = tx.Users.FindUnique(ctx, bob, projection)
		return err
	}); err != nil { t.Fatal(err) }

	parseID := func(value string) golem.UUID {
		parsed, err := golem.ParseUUID(value)
		if err != nil { t.Fatal(err) }
		return parsed
	}
	assertName := func(operation string, row golem.Row[models.User], want string) {
		name, present := golem.Value(row, models.Users.Name).Get()
		if !present || name != want { t.Fatalf("%%s name=%%q present=%%t", operation, name, present) }
	}
	mutate := func(operation string, create func(models.UserCreateInput) (golem.Row[models.User], error), update func(golem.MutationTarget[models.User], models.UserUpdateInput) (golem.Row[models.User], error), upsert func(golem.MutationTarget[models.User], models.UserCreateInput, models.UserUpdateInput) (golem.Row[models.User], error), deleteUser func(golem.MutationTarget[models.User]) (golem.Row[models.User], error), id golem.UUID, before, after string) error {
		row, err := create(models.Users.Create(models.Users.ID.Create(id), models.Users.Name.Create(before)))
		if err != nil { return fmt.Errorf("%%s create: %%w", operation, err) }
		assertName(operation+" create", row, before)
		target := models.Users.ByID.Value(id).And(models.Users.Name.StartsWith("a"))
		row, err = update(target, models.Users.Update(models.Users.Name.Set(after)))
		if err != nil { return fmt.Errorf("%%s update: %%w", operation, err) }
		assertName(operation+" update", row, after)
		row, err = upsert(target, models.Users.Create(models.Users.ID.Create(id), models.Users.Name.Create("wrong-branch")), models.Users.Update(models.Users.Name.Set(after+"-upsert")))
		if err != nil { return fmt.Errorf("%%s upsert: %%w", operation, err) }
		after += "-upsert"
		assertName(operation+" upsert", row, after)
		row, err = deleteUser(target)
		if err != nil { return fmt.Errorf("%%s delete: %%w", operation, err) }
		assertName(operation+" delete", row, after)
		return nil
	}
	if err := mutate("caller",
		func(input models.UserCreateInput) (golem.Row[models.User], error) { return caller.Users.Create(ctx, input, projection) },
		func(target golem.MutationTarget[models.User], input models.UserUpdateInput) (golem.Row[models.User], error) { return caller.Users.Update(ctx, target, input, projection) },
		func(target golem.MutationTarget[models.User], create models.UserCreateInput, update models.UserUpdateInput) (golem.Row[models.User], error) { return caller.Users.Upsert(ctx, target, create, update, projection) },
		func(target golem.MutationTarget[models.User]) (golem.Row[models.User], error) { return caller.Users.Delete(ctx, target, projection) },
		parseID("00000000-0000-0000-0000-000000000011"), "amy", "anna"); err != nil { t.Fatal(err) }
	if err := mutate("system",
		func(input models.UserCreateInput) (golem.Row[models.User], error) { return system.Users.Create(ctx, input, projection) },
		func(target golem.MutationTarget[models.User], input models.UserUpdateInput) (golem.Row[models.User], error) { return system.Users.Update(ctx, target, input, projection) },
		func(target golem.MutationTarget[models.User], create models.UserCreateInput, update models.UserUpdateInput) (golem.Row[models.User], error) { return system.Users.Upsert(ctx, target, create, update, projection) },
		func(target golem.MutationTarget[models.User]) (golem.Row[models.User], error) { return system.Users.Delete(ctx, target, projection) },
		parseID("00000000-0000-0000-0000-000000000012"), "azure", "apricot"); err != nil { t.Fatal(err) }
	if err := caller.Transaction(ctx, func(tx *generated.CallerTx[string]) error {
		return mutate("caller tx",
			func(input models.UserCreateInput) (golem.Row[models.User], error) { return tx.Users.Create(ctx, input, projection) },
			func(target golem.MutationTarget[models.User], input models.UserUpdateInput) (golem.Row[models.User], error) { return tx.Users.Update(ctx, target, input, projection) },
			func(target golem.MutationTarget[models.User], create models.UserCreateInput, update models.UserUpdateInput) (golem.Row[models.User], error) { return tx.Users.Upsert(ctx, target, create, update, projection) },
			func(target golem.MutationTarget[models.User]) (golem.Row[models.User], error) { return tx.Users.Delete(ctx, target, projection) },
			parseID("00000000-0000-0000-0000-000000000013"), "ava", "aria")
	}); err != nil { t.Fatal(err) }
	if err := system.Transaction(ctx, func(tx *generated.SystemTx[string]) error {
		return mutate("system tx",
			func(input models.UserCreateInput) (golem.Row[models.User], error) { return tx.Users.Create(ctx, input, projection) },
			func(target golem.MutationTarget[models.User], input models.UserUpdateInput) (golem.Row[models.User], error) { return tx.Users.Update(ctx, target, input, projection) },
			func(target golem.MutationTarget[models.User], create models.UserCreateInput, update models.UserUpdateInput) (golem.Row[models.User], error) { return tx.Users.Upsert(ctx, target, create, update, projection) },
			func(target golem.MutationTarget[models.User]) (golem.Row[models.User], error) { return tx.Users.Delete(ctx, target, projection) },
			parseID("00000000-0000-0000-0000-000000000014"), "amber", "autumn")
	}); err != nil { t.Fatal(err) }

	batchMutate := func(operation string, create func(models.UserCreateInput) error, updateMany func(golem.Predicate[models.User], models.UserUpdateManyInput) (int64, error), deleteMany func(golem.Predicate[models.User]) (int64, error), id golem.UUID, before, after string, beforeWhere, afterWhere golem.Predicate[models.User], want int64) error {
		if err := create(models.Users.Create(models.Users.ID.Create(id), models.Users.Name.Create(before))); err != nil { return fmt.Errorf("%%s create: %%w", operation, err) }
		count, err := updateMany(beforeWhere, models.Users.UpdateMany(models.Users.Name.Set(after)))
		if err != nil || count != want { return fmt.Errorf("%%s updateMany count=%%d: %%w", operation, count, err) }
		count, err = deleteMany(afterWhere)
		if err != nil || count != want { return fmt.Errorf("%%s deleteMany count=%%d: %%w", operation, count, err) }
		return nil
	}
	if err := batchMutate("caller batch",
		func(input models.UserCreateInput) error { _, err := caller.Users.Create(ctx, input); return err },
		func(where golem.Predicate[models.User], input models.UserUpdateManyInput) (int64, error) { return caller.Users.UpdateMany(ctx, where, input) },
		func(where golem.Predicate[models.User]) (int64, error) { return caller.Users.DeleteMany(ctx, where) },
		parseID("00000000-0000-0000-0000-000000000021"), "abel", "adrian", models.Users.Name.StartsWith("a"), models.Users.Name.StartsWith("a"), 2); err != nil { t.Fatal(err) }
	if err := batchMutate("system batch",
		func(input models.UserCreateInput) error { _, err := system.Users.Create(ctx, input); return err },
		func(where golem.Predicate[models.User], input models.UserUpdateManyInput) (int64, error) { return system.Users.UpdateMany(ctx, where, input) },
		func(where golem.Predicate[models.User]) (int64, error) { return system.Users.DeleteMany(ctx, where) },
		parseID("00000000-0000-0000-0000-000000000022"), "ben", "bruce", models.Users.Name.Eq("ben"), models.Users.Name.Eq("bruce"), 1); err != nil { t.Fatal(err) }
	if err := caller.Transaction(ctx, func(tx *generated.CallerTx[string]) error {
		return batchMutate("caller tx batch",
			func(input models.UserCreateInput) error { _, err := tx.Users.Create(ctx, input); return err },
			func(where golem.Predicate[models.User], input models.UserUpdateManyInput) (int64, error) { return tx.Users.UpdateMany(ctx, where, input) },
			func(where golem.Predicate[models.User]) (int64, error) { return tx.Users.DeleteMany(ctx, where) },
			parseID("00000000-0000-0000-0000-000000000023"), "alex", "andrew", models.Users.Name.StartsWith("a"), models.Users.Name.StartsWith("a"), 1)
	}); err != nil { t.Fatal(err) }
	if err := system.Transaction(ctx, func(tx *generated.SystemTx[string]) error {
		return batchMutate("system tx batch",
			func(input models.UserCreateInput) error { _, err := tx.Users.Create(ctx, input); return err },
			func(where golem.Predicate[models.User], input models.UserUpdateManyInput) (int64, error) { return tx.Users.UpdateMany(ctx, where, input) },
			func(where golem.Predicate[models.User]) (int64, error) { return tx.Users.DeleteMany(ctx, where) },
			parseID("00000000-0000-0000-0000-000000000024"), "carl", "chris", models.Users.Name.Eq("carl"), models.Users.Name.Eq("chris"), 1)
	}); err != nil { t.Fatal(err) }
}
`, "file:"+databasePath)
	runFreshReadSurfaceModule(t, files, false, nil)
}

func TestGeneratedMutationSurfaceRejectsIllegalProgramsAtCompileTime(t *testing.T) {
	artifacts := buildReadSurfaceArtifacts(t)
	base := cloneSourceFiles(artifacts.files)
	base["generated/"+Filename] = string(artifacts.registry)
	tests := []struct {
		name, source string
		diagnostics  []string
	}{
		{
			name: "cross-model field",
			source: `package invalid
import "example.test/app/models"
var _ = models.Users.Select(models.Posts.Title)
`,
			diagnostics: []string{"cannot use", "models.Posts.Title", "Selection[models.User]"},
		},
		{
			name: "bounded arguments on to-one",
			source: `package invalid
import "example.test/app/models"
var _ = models.Posts.Author.Args(models.Users.Take(1))
`,
			diagnostics: []string{"models.Posts.Author.Args undefined", "no field or method Args"},
		},
		{
			name: "forged selector",
			source: `package invalid
import (
	"context"
	"example.test/app/generated"
)
type forgedSelector struct{}
func invalid(system generated.System[string]) {
	_, _ = system.Users.FindUnique(context.Background(), forgedSelector{})
}
`,
			diagnostics: []string{"cannot use forgedSelector", "UniqueSelectorValue[models.User]"},
		},
		{
			name: "read option cannot be a mutation projection",
			source: `package invalid
import (
	"context"
	"example.test/app/generated"
	"example.test/app/models"
)
func invalid(system generated.System[string], input models.UserCreateInput) {
	_, _ = system.Users.Create(context.Background(), input, models.Users.Where(models.Users.Name.Eq("alice")))
}
`,
			diagnostics: []string{"cannot use", "ReadOption[models.User]", "Projection[models.User]"},
		},
		{
			name: "create operation cannot enter update input",
			source: `package invalid
import "example.test/app/models"
var _ = models.Users.Update(models.Users.Name.Create("alice"))
`,
			diagnostics: []string{"cannot use", "CreateValue[models.User]", "UpdateValue[models.User]"},
		},
		{
			name: "wrong scalar Go type",
			source: `package invalid
import "example.test/app/models"
var _ = models.Users.Name.Create(42)
`,
			diagnostics: []string{"cannot use 42", "as string"},
		},
		{
			name: "required to-one disconnect",
			source: `package invalid
import "example.test/app/models"
var _ = models.Posts.Author.Disconnect()
`,
			diagnostics: []string{"models.Posts.Author.Disconnect undefined"},
		},
		{
			name: "to-one create-many",
			source: `package invalid
import "example.test/app/models"
var _ = models.Posts.Author.CreateMany()
`,
			diagnostics: []string{"models.Posts.Author.CreateMany undefined"},
		},
		{
			name: "relation cannot enter update-many",
			source: `package invalid
import "example.test/app/models"
var _ = models.Users.UpdateMany(models.Users.Posts.Set())
`,
			diagnostics: []string{"does not implement golem.UpdateManyValue[models.User]"},
		},
		{
			name: "top-level create-many is absent",
			source: `package invalid
import "example.test/app/models"
var _ = models.Posts.CreateMany()
`,
			diagnostics: []string{"models.Posts.CreateMany undefined"},
		},
		{
			name: "top-level update requires a value",
			source: `package invalid
import "example.test/app/models"
var _ = models.Posts.Update()
`,
			diagnostics: []string{"not enough arguments"},
		},
		{
			name: "top-level update-many requires a scalar value",
			source: `package invalid
import "example.test/app/models"
var _ = models.Posts.UpdateMany()
`,
			diagnostics: []string{"not enough arguments"},
		},
		{
			name: "to-many create requires input",
			source: `package invalid
import "example.test/app/models"
var _ = models.Users.Posts.Create()
`,
			diagnostics: []string{"not enough arguments"},
		},
		{
			name: "to-many create-many requires input",
			source: `package invalid
import "example.test/app/models"
var _ = models.Users.Posts.CreateMany()
`,
			diagnostics: []string{"not enough arguments"},
		},
		{
			name: "to-many connect requires target",
			source: `package invalid
import "example.test/app/models"
var _ = models.Users.Posts.Connect()
`,
			diagnostics: []string{"not enough arguments"},
		},
		{
			name: "to-many disconnect requires target",
			source: `package invalid
import "example.test/app/models"
var _ = models.Users.Posts.Disconnect()
`,
			diagnostics: []string{"not enough arguments"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := cloneSourceFiles(base)
			files["invalid/invalid.go"] = test.source
			runFreshReadSurfaceModule(t, files, true, test.diagnostics)
		})
	}
}

func buildReadSurfaceArtifacts(t *testing.T) readSurfaceArtifacts {
	t.Helper()
	fixture := schematest.New(t)
	var model ir.ModelIR
	if err := json.Unmarshal(fixture.Bundle.Model().Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	var contract ir.ContractIR
	if err := json.Unmarshal(fixture.Bundle.Contract().Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	for index := range model.Models {
		model.Models[index].Go = ir.GoNamedTypeIR{PackagePath: "example.test/app/models", Name: model.Models[index].LogicalName}
		for contractIndex := range contract.Models {
			if contract.Models[contractIndex].ModelID != model.Models[index].ID {
				continue
			}
			primary := model.Models[index].PrimaryKey
			if primary == nil {
				t.Fatalf("model %s has no primary identity", model.Models[index].LogicalName)
			}
			contract.Models[contractIndex].Selectors = []ir.SelectorContractIR{{
				KeyID: primary.ID, Kind: ir.KeyPrimary, Name: "ID", Fields: append([]ir.FieldID(nil), primary.Fields...),
			}}
		}
	}
	compilation := ir.CompilationIR{Model: model, Contract: contract}
	stamp := &modelcodegen.FinalStamp{GenerationDigest: readSurfaceGenerationDigest, GeneratorVersion: "acceptance", TemplateABIVersion: "acceptance"}
	generatedModels, err := modelcodegen.Emit(modelcodegen.Request{
		Compilation: compilation,
		Packages:    []modelcodegen.PackageSpec{{ImportPath: "example.test/app/models", PackageName: "models"}},
		FinalStamp:  stamp,
	})
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := ir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := ir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	providers := []ProviderInput{
		providerInput(t, fixture.SQLite),
		providerInput(t, fixture.PostgreSQL),
	}
	sqliteManifest, sqliteFiles, sqliteManifestBytes := reviewedSQLiteReadSurfaceFixture(t, fixture.SQLite, modelFingerprint)
	providers[0].MigrationManifest = sqliteManifestBytes
	generatedRegistry, err := Emit(Request{
		AppPackage:         modelcodegen.PackageSpec{ImportPath: "example.test/app/generated", PackageName: "generated"},
		ModelPackages:      []modelcodegen.PackageSpec{{ImportPath: "example.test/app/models", PackageName: "models"}},
		Actor:              ir.GoNamedTypeIR{PackagePath: "example.test/app/security", Name: "Actor"},
		GenerationDigest:   readSurfaceGenerationDigest,
		GeneratorVersion:   "acceptance",
		TemplateABIVersion: "acceptance",
		Schema:             SchemaInput{Model: model, Contract: contract, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint, Providers: providers},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"security/actor.go": "package security\ntype Actor struct { Prefix string }\n",
		"models/models.go":  "package models\ntype User struct{}\ntype Post struct{}\n",
		"models/policies.go": `package models
import (
	"example.test/app/security"
	"github.com/eleven-am/golem/go/golem"
)
func GolemGeneratedBindings() golem.PackageBindings[security.Actor] {
	user := golem.GeneratedPolicyBinding[security.Actor, User](GolemGeneratedUserDescriptor.Metadata().ModelID(), func(actor security.Actor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[User]()
		rules.CanRead(Users.Name.StartsWith(actor.Prefix))
		rules.CanCreate(golem.All[User]())
		rules.CanUpdate(golem.All[User]())
		rules.CanDelete(golem.All[User]())
		return rules.Freeze(GolemGeneratedUserDescriptor.Metadata().ModelID())
	})
	post := golem.GeneratedPolicyBinding[security.Actor, Post](GolemGeneratedPostDescriptor.Metadata().ModelID(), func(security.Actor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[Post]()
		rules.CanRead(golem.All[Post]())
		rules.CanCreate(golem.All[Post]())
		rules.CanUpdate(golem.All[Post]())
		rules.CanDelete(golem.All[Post]())
		return rules.Freeze(GolemGeneratedPostDescriptor.Metadata().ModelID())
	})
	return golem.GeneratedStampedPackageBindings[security.Actor](golem.SchemaDigest{31: 1}, []golem.PolicyBinding[security.Actor]{user, post}, nil)
}
`,
		"generated/doc.go": "package generated\n",
	}
	for _, file := range generatedModels.Files {
		files[filepath.Join("models", filepath.Base(file.Path))] = string(file.Source)
	}
	return readSurfaceArtifacts{files: files, registry: generatedRegistry.Source, sqliteManifest: sqliteManifest, sqliteFiles: sqliteFiles}
}

func reviewedSQLiteReadSurfaceFixture(t *testing.T, after physical.PhysicalSchema, afterModel ir.Fingerprint) (migration.Manifest, map[string][]byte, []byte) {
	t.Helper()
	after, err := physical.Normalize(after)
	if err != nil {
		t.Fatal(err)
	}
	before, err := physical.Normalize(physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: after.Provider, Namespace: after.Namespace,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.ValidatePlan(plan, nil); err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, err := physical.PhysicalFingerprint(before)
	if err != nil {
		t.Fatal(err)
	}
	afterFingerprint, err := physical.PhysicalFingerprint(after)
	if err != nil {
		t.Fatal(err)
	}
	allowlist, err := physical.UnmanagedAllowlistFingerprint(after)
	if err != nil {
		t.Fatal(err)
	}
	risks := make([]migration.OperationRisk, len(plan.Operations))
	for index, operation := range plan.Operations {
		risks[index] = migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk}
	}
	entry := migration.ManifestEntry{
		ID: "0001_initial", Operations: plan.Operations, Phases: plan.Phases, Risks: risks,
		BeforeModel: migration.Checksum([]byte("empty read-surface fixture model")), AfterModel: migration.Digest(afterModel),
		BeforePhysical: migration.Digest(beforeFingerprint.String()), AfterPhysical: migration.Digest(afterFingerprint.String()),
		BeforeSnapshot: before, AfterSnapshot: after, UnmanagedAllowlistDigest: migration.Digest(allowlist.String()),
	}
	script, err := sqliteprovider.New().RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"sqlite/0001_initial.sql": []byte(script.SQL())}
	entry.Files = []migration.FileChecksum{{Path: "sqlite/0001_initial.sql", SHA256: migration.Checksum(files["sqlite/0001_initial.sql"])}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest := migration.Manifest{
		FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion,
		HashAlgorithm: "sha256", GeneratorVersion: "registry-read-surface-fixture-v1", Provider: after.Provider,
		Entries: []migration.ManifestEntry{entry},
	}
	encoded, err := migration.EncodeManifest(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, files, encoded
}

func providerInput(t *testing.T, schema physical.PhysicalSchema) ProviderInput {
	t.Helper()
	fingerprint, err := physical.PhysicalFingerprint(schema)
	if err != nil {
		t.Fatal(err)
	}
	systemFingerprint, err := physical.SystemFingerprint(schema.Provider, schema.System)
	if err != nil {
		t.Fatal(err)
	}
	return ProviderInput{Schema: schema, Fingerprint: ir.Fingerprint(fingerprint.String()), SystemFingerprint: ir.Fingerprint(systemFingerprint.String())}
}

func runFreshReadSurfaceModule(t *testing.T, files map[string]string, wantFailure bool, diagnostics []string) {
	t.Helper()
	root := t.TempDir()
	module := fmt.Sprintf("module example.test/app\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", moduleDir(t))
	writeRegistryTestFile(t, filepath.Join(root, "go.mod"), module)
	for path, source := range files {
		writeRegistryTestFile(t, filepath.Join(root, path), source)
	}
	command := exec.Command("go", "test", "-mod=mod", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if !wantFailure {
		if err != nil {
			t.Fatalf("fresh generated consumer failed: %v\n%s", err, output)
		}
		return
	}
	if err == nil {
		t.Fatalf("invalid generated consumer unexpectedly compiled\n%s", output)
	}
	text := string(output)
	for _, diagnostic := range diagnostics {
		if !strings.Contains(text, diagnostic) {
			t.Fatalf("compiler output lacks %q:\n%s", diagnostic, text)
		}
	}
}

func cloneSourceFiles(files map[string]string) map[string]string {
	result := make(map[string]string, len(files))
	for path, source := range files {
		result[path] = source
	}
	return result
}
