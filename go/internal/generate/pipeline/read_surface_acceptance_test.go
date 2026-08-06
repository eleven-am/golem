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

func TestFreshSocialModuleGeneratesCompilesAndConstructsGraphQLServer(t *testing.T) {
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
	Comments []Comment §db:"-" golem:"relation=has_many;fields=id;references=author_id"§
	FriendshipsFrom []Friendship §db:"-" golem:"relation=has_many;name=Origin;fields=id;references=user_id"§
	FriendshipsTo []Friendship §db:"-" golem:"relation=has_many;name=Destination;fields=id;references=friend_id"§
}

type Post struct {
	_ struct{} §golem:"model;id=acceptance.Post;table=posts"§
	_ struct{} §golem:"index=idx_posts_author(author_id)"§
	ID golem.UUID §db:"id" golem:"id=acceptance.Post.ID;pk"§
	AuthorID golem.UUID §db:"author_id" golem:"id=acceptance.Post.AuthorID"§
	Title string §db:"title" golem:"id=acceptance.Post.Title"§
	Author *User §db:"-" golem:"relation=belongs_to;fields=author_id;references=id"§
	Comments []Comment §db:"-" golem:"relation=has_many;fields=id;references=post_id"§
	PostTags []PostTag §db:"-" golem:"relation=has_many;fields=id;references=post_id"§
}

type Comment struct {
	_ struct{} §golem:"model;id=acceptance.Comment;table=comments"§
	_ struct{} §golem:"index=idx_comments_post_parent(post_id,parent_id)"§
	ID golem.UUID §db:"id" golem:"pk"§
	PostID golem.UUID §db:"post_id"§
	AuthorID golem.UUID §db:"author_id"§
	ParentID golem.Null[golem.UUID] §db:"parent_id"§
	Body string §db:"body"§
	Post *Post §db:"-" golem:"relation=belongs_to;fields=post_id;references=id"§
	Author *User §db:"-" golem:"relation=belongs_to;fields=author_id;references=id"§
	ReplyTo *Comment §db:"-" golem:"relation=belongs_to;name=ReplyTree;fields=parent_id;references=id"§
	Replies []Comment §db:"-" golem:"relation=has_many;name=ReplyTree;fields=id;references=parent_id"§
}

type Friendship struct {
	_ struct{} §golem:"model;id=acceptance.Friendship;table=friendships"§
	_ struct{} §golem:"primary=pk_friendships(user_id,friend_id)"§
	_ struct{} §golem:"index=idx_friendships_friend_user(friend_id,user_id)"§
	UserID golem.UUID §db:"user_id"§
	FriendID golem.UUID §db:"friend_id"§
	User *User §db:"-" golem:"relation=belongs_to;name=Origin;fields=user_id;references=id"§
	Friend *User §db:"-" golem:"relation=belongs_to;name=Destination;fields=friend_id;references=id"§
}

type Tag struct {
	_ struct{} §golem:"model;id=acceptance.Tag;table=tags"§
	_ struct{} §golem:"unique=uq_tags_name(name)"§
	ID golem.UUID §db:"id" golem:"pk"§
	Name string §db:"name" golem:"type=varchar(64)"§
	PostTags []PostTag §db:"-" golem:"relation=has_many;fields=name;references=tag_name"§
}

type PostTag struct {
	_ struct{} §golem:"model;id=acceptance.PostTag;table=post_tags"§
	_ struct{} §golem:"primary=pk_post_tags(post_id,tag_name)"§
	PostID golem.UUID §db:"post_id"§
	TagName string §db:"tag_name" golem:"type=varchar(64)"§
	Post *Post §db:"-" golem:"relation=belongs_to;fields=post_id;references=id"§
	Tag *Tag §db:"-" golem:"relation=belongs_to;fields=tag_name;references=name"§
}

func (User) DefinePolicy(rules *golem.Rules[User], value actor.Actor) {
	rules.CanRead(Users.Name.StartsWith(value.Prefix))
}

func (Post) DefinePolicy(rules *golem.Rules[Post], _ actor.Actor) {
	rules.CanRead(golem.All[Post]())
}

func (Comment) DefinePolicy(rules *golem.Rules[Comment], _ actor.Actor) { rules.CanRead(golem.All[Comment]()) }
func (Friendship) DefinePolicy(rules *golem.Rules[Friendship], _ actor.Actor) { rules.CanRead(golem.All[Friendship]()) }
func (Tag) DefinePolicy(rules *golem.Rules[Tag], _ actor.Actor) { rules.CanRead(golem.All[Tag]()) }
func (PostTag) DefinePolicy(rules *golem.Rules[PostTag], _ actor.Actor) { rules.CanRead(golem.All[PostTag]()) }
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
	golem.Model[models.Comment](schema)
	golem.Model[models.Friendship](schema)
	golem.Model[models.Tag](schema)
	golem.Model[models.PostTag](schema)
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
		if artifact.Kind != manifest.ArtifactModelGo && artifact.Kind != manifest.ArtifactBindingsGo && artifact.Kind != manifest.ArtifactRegistryGo && artifact.Kind != manifest.ArtifactGraphQLGo && artifact.Kind != manifest.ArtifactGraphQLSDL {
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
	"net/http"
	"net/http/httptest"
	"strings"
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

	server, err := application.GraphQL(app.GraphQLConfig[string]{
		PrincipalFromContext: func(context.Context) (string, bool) { return "principal", true },
		ReportInternalError: func(_ context.Context, err error) { t.Errorf("trusted GraphQL error: %%v", err) },
	})
	if err != nil { t.Fatal(err) }
	if server.SDL() == "" || server.Handler() == nil { t.Fatal("generated GraphQL server is incomplete") }
	if server.ContractFingerprint() != app.GolemGeneratedSchemaBundle().Contract().Fingerprint() { t.Fatal("GraphQL contract fingerprint mismatch") }
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`+"`"+`{"query":"query { users(take: 2) { id name } }"}`+"`"+`))
	request.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, `+"`"+`"name":"alice"`+"`"+`) || strings.Contains(body, `+"`"+`"name":"bob"`+"`"+`) || strings.Contains(body, `+"`"+`"errors"`+"`"+`) {
		t.Fatalf("GraphQL response code=%%d body=%%s", recorder.Code, body)
	}
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
