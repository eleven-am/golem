package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/physical"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestFreshGeneratedExecutableRunsMixedOrdinaryAndCustomDocument(t *testing.T) {
	root := t.TempDir()
	writePipelineAcceptanceFile(t, root, "go.mod", fmt.Sprintf("module example.test/customapp\n\ngo 1.23\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => %s\n", moduleRoot(t)))
	writePipelineAcceptanceFile(t, root, "app/model.go", `package app

import (
 "context"
 "github.com/eleven-am/golem/go/golem"
)
type Principal struct{ ID string }
type User struct {
 _ struct{} `+"`golem:\"model;id=custom.User;table=users\"`"+`
 ID int64 `+"`db:\"id\" golem:\"pk\"`"+`
 Name string `+"`db:\"name\"`"+`
}
type EchoArgs struct { Message string `+"`golem:\"graphql=message\"`"+` }
type GreetingArgs struct { Prefix string `+"`golem:\"graphql=prefix\"`"+` }
func (User) DefinePolicy(r *golem.Rules[User], _ Principal) { r.CanRead(golem.All[User]()) }
func (User) DefineGraphQL(g *golem.GraphQLModel[User]) { golem.ComputedField(g, "greeting", golem.GraphQLString().NonNull(), User{}.Greeting, golem.Requires(Users.Name)) }
func (User) Greeting(_ context.Context, row golem.Row[User], args GreetingArgs) (string, error) { name, _ := golem.Value(row, Users.Name).Get(); return args.Prefix+name, nil }
func DefineSchema(s *golem.Schema) { golem.SchemaName(s, "custom_app"); golem.Actor[Principal](s); golem.Model[User](s); golem.Providers(s, golem.SQLite) }
func DefineGraphQL(g *golem.GraphQLSchema) { golem.Query(g, "echo", Echo) }
func Echo(_ context.Context, caller *Caller[Principal], args EchoArgs) (string, error) { if caller == nil { return "", context.Canceled }; return "custom:"+args.Message, nil }
`)
	// The first compile needs only the generated Caller type's name. Publication
	// atomically replaces this bootstrap file with the real registry artifact.
	writePipelineAcceptanceFile(t, root, "app/zz_golem_registry.gen.go", "package app\ntype Caller[P any] struct{}\n")
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	tidy.Env = append(os.Environ(), "GOWORK=off")
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("prepare module: %v\n%s", err, output)
	}
	result, err := Build(context.Background(), Request{
		Compile:    compile.Config{Dir: filepath.Join(root, "app"), Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.test/customapp/app", PackageName: "app", Directory: filepath.Join(root, "app")},
		Lowerers:   []physical.Lowerer{sqliteprovider.New()}, Env: []string{"GOWORK=off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range result.Prospective.Artifacts {
		if artifact.Kind == manifest.ArtifactModelGo || artifact.Kind == manifest.ArtifactBindingsGo || artifact.Kind == manifest.ArtifactRegistryGo || artifact.Kind == manifest.ArtifactGraphQLGo || artifact.Kind == manifest.ArtifactGraphQLSDL {
			writePipelineAcceptanceFile(t, root, artifact.Path, string(artifact.Content))
		}
	}
	databasePath := filepath.Join(t.TempDir(), "custom.db")
	database, _, err := sqliteprovider.New().Open(context.Background(), "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.New().ApplyInitial(context.Background(), database, result.Providers[0].Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO "users"("id","name") VALUES (?,?)`, 1, "alice"); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	writePipelineAcceptanceFile(t, root, "acceptance/custom_test.go", fmt.Sprintf(`package acceptance_test
import (
 "context"; "net/http"; "net/http/httptest"; "strings"; "testing"
 "example.test/customapp/app"; "github.com/eleven-am/golem/go/golem"; "github.com/jmoiron/sqlx"
)
func TestMixed(t *testing.T) {
 db, err := sqlx.Open("sqlite", %q); if err != nil { t.Fatal(err) }; defer db.Close()
 application, err := app.Open(context.Background(), app.Config[app.Principal]{DB: db, Provider: golem.SQLite, ResolvePrincipal: func(context.Context, app.Principal)(app.Principal,error){ return app.Principal{ID:"p"},nil }}); if err != nil { t.Fatal(err) }
 server, err := application.GraphQL(app.GraphQLConfig[app.Principal]{PrincipalFromContext: func(context.Context)(app.Principal,bool){ return app.Principal{ID:"p"},true }, ReportInternalError: func(_ context.Context, err error){ t.Errorf("internal: %%v",err) }}); if err != nil { t.Fatal(err) }
 request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`+"`"+`{"query":"query { users { name greeting(prefix: \"hi:\") } echo(message: \"ok\") }"}`+"`"+`)); request.Header.Set("Content-Type","application/json")
 recorder := httptest.NewRecorder(); server.Handler().ServeHTTP(recorder, request); body := recorder.Body.String()
 if recorder.Code != 200 || !strings.Contains(body, `+"`"+`"name":"alice"`+"`"+`) || !strings.Contains(body, `+"`"+`"greeting":"hi:alice"`+"`"+`) || !strings.Contains(body, `+"`"+`"echo":"custom:ok"`+"`"+`) || strings.Contains(body, `+"`"+`"errors"`+"`"+`) { t.Fatalf("code=%%d body=%%s", recorder.Code, body) }
}
`, "file:"+databasePath+"?_pragma=foreign_keys(1)"))
	command := exec.Command("go", "test", "-mod=mod", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh custom consumer failed: %v\n%s", err, output)
	}
}
