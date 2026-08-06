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
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

const readSurfaceGenerationDigest = "0000000000000000000000000000000000000000000000000000000000000001"

type readSurfaceArtifacts struct {
	files    map[string]string
	registry []byte
}

func TestGeneratedReadSurfaceExecutesEveryCallerAndSystemOperationFromFreshModule(t *testing.T) {
	artifacts := buildReadSurfaceArtifacts(t)
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "generated-read-surface.db")
	fixture := schematest.New(t)
	database, _, err := sqliteprovider.New().Open(ctx, "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
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
	"testing"

	"example.test/app/generated"
	"example.test/app/models"
	"example.test/app/security"
	"github.com/eleven-am/golem/go/golem"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/jmoiron/sqlx"
)

func TestEveryGeneratedReadOperation(t *testing.T) {
	ctx := context.Background()
	database, err := sqlx.Open("sqlite", %q)
	if err != nil { t.Fatal(err) }
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = database.Close() })
	application, err := generated.Open(ctx, generated.Config[string]{
		DB: database,
		Provider: golem.SQLite,
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
}
`, "file:"+databasePath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate")
	runFreshReadSurfaceModule(t, files, false, nil)
}

func TestGeneratedReadSurfaceRejectsInvalidProgramsAtCompileTime(t *testing.T) {
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
			name: "unsupported generated method",
			source: `package invalid
import "example.test/app/generated"
func invalid(caller *generated.Caller[string]) { _ = caller.Users.Create }
`,
			diagnostics: []string{"caller.Users.Create undefined", "no field or method Create"},
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
		return rules.Freeze(GolemGeneratedUserDescriptor.Metadata().ModelID())
	})
	post := golem.GeneratedPolicyBinding[security.Actor, Post](GolemGeneratedPostDescriptor.Metadata().ModelID(), func(security.Actor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[Post]()
		rules.CanRead(golem.All[Post]())
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
	return readSurfaceArtifacts{files: files, registry: generatedRegistry.Source}
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
	module := fmt.Sprintf("module example.test/app\n\ngo 1.23\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", moduleDir(t))
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
