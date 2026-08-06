package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/physical"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestFreshExternalModuleBuildsPipelineArtifactsAndExecutesGeneratedReadClients(t *testing.T) {
	root := t.TempDir()
	writePipelineAcceptanceFile(t, root, "go.mod", fmt.Sprintf(`module example.test/social

go 1.23

require github.com/eleven-am/golem/go v0.0.0

replace github.com/eleven-am/golem/go => %s
`, moduleRoot(t)))
	writePipelineAcceptanceFile(t, root, "actor/actor.go", `package actor
type Actor struct { Prefix string }
`)
	modelsSource := strings.ReplaceAll(`package models

import (
	"example.test/social/actor"
	"github.com/eleven-am/golem/go/golem"
)

type User struct {
	_ struct{} §golem:"model;id=acceptance.User;table=users"§
	ID golem.UUID §db:"id" golem:"id=acceptance.User.ID;pk"§
	Name string §db:"name" golem:"id=acceptance.User.Name"§
	Posts []Post §db:"-" golem:"relation=has_many;fields=id;references=author_id"§
}

type Post struct {
	_ struct{} §golem:"model;id=acceptance.Post;table=posts"§
	_ struct{} §golem:"index=idx_posts_author(author_id)"§
	ID golem.UUID §db:"id" golem:"id=acceptance.Post.ID;pk"§
	AuthorID golem.UUID §db:"author_id" golem:"id=acceptance.Post.AuthorID"§
	Title string §db:"title" golem:"id=acceptance.Post.Title"§
	Author *User §db:"-" golem:"relation=belongs_to;fields=author_id;references=id"§
}

func (User) DefinePolicy(rules *golem.Rules[User], value actor.Actor) {
	rules.CanRead(Users.Name.StartsWith(value.Prefix))
}

func (Post) DefinePolicy(rules *golem.Rules[Post], _ actor.Actor) {
	rules.CanRead(golem.All[Post]())
}
`, "§", "`")
	writePipelineAcceptanceFile(t, root, "models/models.go", modelsSource)
	writePipelineAcceptanceFile(t, root, "schema/schema.go", `package schema

import (
	"example.test/social/actor"
	"example.test/social/models"
	"github.com/eleven-am/golem/go/golem"
)

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "read_surface_acceptance")
	golem.Actor[actor.Actor](schema)
	golem.Model[models.User](schema)
	golem.Model[models.Post](schema)
	golem.Providers(schema, golem.SQLite)
}
`)
	writePipelineAcceptanceFile(t, root, "app/doc.go", "package app\n")
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	tidy.Env = append(os.Environ(), "GOWORK=off")
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("prepare fresh module: %v\n%s", err, output)
	}

	result, err := Build(context.Background(), Request{
		Compile:    compile.Config{Dir: root, Pattern: "./schema", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.test/social/app", PackageName: "app", Directory: filepath.Join(root, "app")},
		Lowerers:   []physical.Lowerer{sqliteprovider.New()},
		Env:        []string{"GOWORK=off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range result.Prospective.Artifacts {
		if artifact.Kind != manifest.ArtifactModelGo && artifact.Kind != manifest.ArtifactBindingsGo && artifact.Kind != manifest.ArtifactRegistryGo {
			continue
		}
		writePipelineAcceptanceFile(t, root, artifact.Path, string(artifact.Content))
	}
	if len(result.Providers) != 1 {
		t.Fatalf("providers=%d", len(result.Providers))
	}
	databasePath := filepath.Join(t.TempDir(), "pipeline-read-surface.db")
	database, _, err := sqliteprovider.New().Open(context.Background(), "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.New().ApplyInitial(context.Background(), database, result.Providers[0].Schema); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	for _, row := range [][2]string{
		{"00000000-0000-0000-0000-000000000001", "alice"},
		{"00000000-0000-0000-0000-000000000002", "bob"},
	} {
		if _, err := database.ExecContext(context.Background(), `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	writePipelineAcceptanceFile(t, root, "acceptance/read_surface_test.go", fmt.Sprintf(`package acceptance_test

import (
	"context"
	"testing"

	"example.test/social/actor"
	"example.test/social/app"
	"example.test/social/models"
	"github.com/eleven-am/golem/go/golem"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/jmoiron/sqlx"
)

func TestGeneratedCallerAndSystemReads(t *testing.T) {
	ctx := context.Background()
	database, err := sqlx.Open("sqlite", %q)
	if err != nil { t.Fatal(err) }
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = database.Close() })
	application, err := app.Open(ctx, app.Config[string]{
		DB: database, Provider: golem.SQLite,
		ReadLimits: golemruntime.ReadLimits{MaxTake: 2},
		ResolvePrincipal: func(context.Context, string) (actor.Actor, error) { return actor.Actor{Prefix: "a"}, nil },
	})
	if err != nil { t.Fatal(err) }
	aliceID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000001")
	bobID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000002")
	projection := models.Users.Select(models.Users.ID, models.Users.Name)
	order := models.Users.OrderBy(models.Users.Name.Asc())
	caller, err := application.ForPrincipal(ctx, "principal")
	if err != nil { t.Fatal(err) }
	rows, err := caller.Users.FindMany(ctx, order, projection)
	if err != nil || len(rows) != 1 { t.Fatalf("caller findMany rows=%%d err=%%v", len(rows), err) }
	first, found, err := caller.Users.FindFirst(ctx, order, projection)
	if err != nil || !found { t.Fatalf("caller findFirst found=%%t err=%%v", found, err) }
	if name, ok := golem.Value(first, models.Users.Name).Get(); !ok || name != "alice" { t.Fatalf("caller first=%%q present=%%t", name, ok) }
	unique, err := caller.Users.FindUnique(ctx, models.Users.ByID.Value(aliceID), projection)
	if err != nil { t.Fatal(err) }
	if name, ok := golem.Value(unique, models.Users.Name).Get(); !ok || name != "alice" { t.Fatalf("caller unique=%%q present=%%t", name, ok) }
	count, err := caller.Users.Count(ctx)
	if err != nil || count != 1 { t.Fatalf("caller count=%%d err=%%v", count, err) }
	system := application.System()
	rows, err = system.Users.FindMany(ctx, order, projection)
	if err != nil || len(rows) != 2 { t.Fatalf("system findMany rows=%%d err=%%v", len(rows), err) }
	first, found, err = system.Users.FindFirst(ctx, order, projection)
	if err != nil || !found { t.Fatalf("system findFirst found=%%t err=%%v", found, err) }
	if name, ok := golem.Value(first, models.Users.Name).Get(); !ok || name != "alice" { t.Fatalf("system first=%%q present=%%t", name, ok) }
	unique, err = system.Users.FindUnique(ctx, models.Users.ByID.Value(bobID), projection)
	if err != nil { t.Fatal(err) }
	if name, ok := golem.Value(unique, models.Users.Name).Get(); !ok || name != "bob" { t.Fatalf("system unique=%%q present=%%t", name, ok) }
	count, err = system.Users.Count(ctx)
	if err != nil || count != 2 { t.Fatalf("system count=%%d err=%%v", count, err) }
}
`, "file:"+databasePath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"))

	command := exec.Command("go", "test", "-mod=mod", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh pipeline consumer failed: %v\n%s", err, output)
	}
}

func writePipelineAcceptanceFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
