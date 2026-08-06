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

func TestFreshActiveExecutablePreservesComputedFailurePathsAndExactCustomResults(t *testing.T) {
	root := t.TempDir()
	writePipelineAcceptanceFile(t, root, "go.mod", fmt.Sprintf("module example.test/activep5\n\ngo 1.23\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => %s\n", moduleRoot(t)))
	writePipelineAcceptanceFile(t, root, "app/model.go", `package app

import (
 "context"
 "errors"
 "time"
 "github.com/eleven-am/golem/go/golem"
)
type Principal struct{ ID string }
type Status string
const ( StatusActive Status = "active"; StatusDisabled Status = "disabled" )
func (Status) GolemEnum() golem.EnumSpec[Status] { return golem.DefineEnum(
 golem.EnumValue(StatusActive, golem.GraphQLValue("ACTIVE")),
 golem.EnumValue(StatusDisabled, golem.GraphQLValue("DISABLED")),
) }
type User struct {
 _ struct{} `+"`golem:\"model;id=active.User;table=users\"`"+`
 ID int64 `+"`db:\"id\" golem:\"pk\"`"+`
 Name string `+"`db:\"name\"`"+`
 Status Status `+"`db:\"status\"`"+`
}
type EmptyArgs struct{}
type StatusArgs struct { Status Status `+"`golem:\"graphql=status\"`"+` }
type Meta struct { Count int64 `+"`json:\"count\"`"+` }
func (User) DefinePolicy(r *golem.Rules[User], _ Principal) { r.CanRead(golem.All[User]()); r.CanCreate(golem.All[User]()); r.CanUpdate(golem.All[User]()) }
func (User) DefineGraphQL(g *golem.GraphQLModel[User]) {
 golem.ComputedField(g, "nullableFailure", golem.GraphQLString(), User{}.NullableFailure, golem.Requires(Users.Name))
 golem.ComputedField(g, "nonNullFailure", golem.GraphQLString().NonNull(), User{}.NonNullFailure, golem.Requires(Users.Name))
 golem.ComputedField(g, "invalidUTF8", golem.GraphQLString(), User{}.InvalidUTF8, golem.Requires(Users.Name))
	 golem.ComputedField(g, "invalidList", golem.GraphQLList(golem.GraphQLString()), User{}.InvalidList, golem.Requires(Users.Name))
	 golem.ComputedField(g, "invalidStrictList", golem.GraphQLList(golem.GraphQLString().NonNull()).NonNull(), User{}.InvalidStrictList, golem.Requires(Users.Name))
	 golem.ComputedField(g, "invalidNullableStrictList", golem.GraphQLList(golem.GraphQLString().NonNull()), User{}.InvalidNullableStrictList, golem.Requires(Users.Name))
	 golem.ComputedField(g, "computedStatus", golem.GraphQLEnum[Status]().NonNull(), User{}.ComputedStatus, golem.Requires(Users.Status))
}
func (User) NullableFailure(context.Context, golem.Row[User], EmptyArgs) (string,error) { return "", errors.New("private nullable failure") }
func (User) NonNullFailure(context.Context, golem.Row[User], EmptyArgs) (string,error) { return "", errors.New("private non-null failure") }
func (User) InvalidUTF8(context.Context, golem.Row[User], EmptyArgs) (string,error) { return string([]byte{0xff}), nil }
func (User) InvalidList(context.Context, golem.Row[User], EmptyArgs) ([]string,error) { return []string{"ok",string([]byte{0xff}),"still"}, nil }
func (User) InvalidStrictList(context.Context, golem.Row[User], EmptyArgs) ([]string,error) { return []string{"ok",string([]byte{0xff}),"still"}, nil }
func (User) InvalidNullableStrictList(context.Context, golem.Row[User], EmptyArgs) ([]string,error) { return []string{"ok",string([]byte{0xff}),"still"}, nil }
func (User) ComputedStatus(context.Context, golem.Row[User], EmptyArgs) (Status,error) { return StatusActive,nil }
func DefineSchema(s *golem.Schema) { golem.SchemaName(s,"active_p5"); golem.Actor[Principal](s); golem.Model[User](s); golem.Providers(s,golem.SQLite) }
func DefineGraphQL(g *golem.GraphQLSchema) {
 golem.Query(g,"failCustom",FailCustom)
 golem.Query(g,"stringValues",StringValues)
 golem.Query(g,"intValues",IntValues)
 golem.Query(g,"bigValues",BigValues)
 golem.Query(g,"decimalValues",DecimalValues)
 golem.Query(g,"nestedValues",NestedValues)
 golem.Query(g,"statusValues",StatusValues)
 golem.Query(g,"echoStatus",EchoStatus)
 golem.Query(g,"invalidStatus",InvalidStatus)
 golem.Query(g,"nullableValues",NullableValues)
 golem.Query(g,"nestedNullableValues",NestedNullableValues)
 golem.Query(g,"maybeString",MaybeString)
	 golem.Query(g,"invalidDateTime",InvalidDateTime)
	 golem.Query(g,"jsonValue",JSONValue)
	 golem.Mutation(g,"nullableMutationFailure",NullableMutationFailure)
	 golem.Mutation(g,"nonNullMutationFailure",NonNullMutationFailure)
}
func FailCustom(context.Context,*Caller[Principal],EmptyArgs)(string,error){ return "",errors.New("private custom failure") }
func StringValues(context.Context,*Caller[Principal],EmptyArgs)([]string,error){ return []string{"a","b"},nil }
func IntValues(context.Context,*Caller[Principal],EmptyArgs)([]int32,error){ return []int32{1,2},nil }
func BigValues(context.Context,*Caller[Principal],EmptyArgs)([]int64,error){ return []int64{9007199254740993},nil }
func DecimalValues(context.Context,*Caller[Principal],EmptyArgs)([]golem.Decimal,error){ v,_:=golem.NewDecimal(12345,2); return []golem.Decimal{v},nil }
func NestedValues(context.Context,*Caller[Principal],EmptyArgs)([][]string,error){ return [][]string{{"x"},{"y","z"}},nil }
func StatusValues(context.Context,*Caller[Principal],EmptyArgs)([]Status,error){ return []Status{StatusActive,StatusDisabled},nil }
func EchoStatus(_ context.Context,_ *Caller[Principal],args StatusArgs)(Status,error){ if args.Status!=StatusActive { return "",errors.New("enum input was not mapped to wire value") }; return args.Status,nil }
func InvalidStatus(context.Context,*Caller[Principal],EmptyArgs)(Status,error){ return Status("forged"),nil }
func NullableValues(context.Context,*Caller[Principal],EmptyArgs)([]*string,error){ value:="present"; return []*string{&value,nil},nil }
func NestedNullableValues(context.Context,*Caller[Principal],EmptyArgs)([][]*string,error){ value:="nested"; return [][]*string{{&value,nil}},nil }
func MaybeString(context.Context,*Caller[Principal],EmptyArgs)(*string,error){ value:="maybe"; return &value,nil }
func InvalidDateTime(context.Context,*Caller[Principal],EmptyArgs)(time.Time,error){ return time.Date(2025,1,1,0,0,0,1,time.UTC),nil }
func JSONValue(context.Context,*Caller[Principal],EmptyArgs)(golem.JSON[Meta],error){ return golem.NewJSONDocument[Meta]([]byte(`+"`{\"count\":9007199254740993}`"+`)) }
var NullableMutationCalls int
var NonNullMutationCalls int
func NullableMutationFailure(context.Context,*Caller[Principal],EmptyArgs)(*string,error){ NullableMutationCalls++; return nil,errors.New("private nullable mutation") }
func NonNullMutationFailure(context.Context,*Caller[Principal],EmptyArgs)(string,error){ NonNullMutationCalls++; return "",errors.New("private non-null mutation") }
`)
	writePipelineAcceptanceFile(t, root, "app/zz_golem_registry.gen.go", "package app\ntype Caller[P any] struct{}\n")
	prepare := exec.Command("go", "mod", "tidy")
	prepare.Dir, prepare.Env = root, append(os.Environ(), "GOWORK=off")
	if output, err := prepare.CombinedOutput(); err != nil {
		t.Fatalf("prepare module: %v\n%s", err, output)
	}
	result, err := Build(context.Background(), Request{
		Compile:    compile.Config{Dir: filepath.Join(root, "app"), Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.test/activep5/app", PackageName: "app", Directory: filepath.Join(root, "app")},
		Lowerers:   []physical.Lowerer{sqliteprovider.New()}, Env: []string{"GOWORK=off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var generatedSDL string
	for _, artifact := range result.Prospective.Artifacts {
		switch artifact.Kind {
		case manifest.ArtifactModelGo, manifest.ArtifactBindingsGo, manifest.ArtifactRegistryGo, manifest.ArtifactGraphQLGo, manifest.ArtifactGraphQLSDL:
			writePipelineAcceptanceFile(t, root, artifact.Path, string(artifact.Content))
			if artifact.Kind == manifest.ArtifactGraphQLSDL {
				generatedSDL = string(artifact.Content)
			}
		}
	}
	databasePath := filepath.Join(t.TempDir(), "active.db")
	database, _, err := sqliteprovider.New().Open(context.Background(), "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.New().ApplyInitial(context.Background(), database, result.Providers[0].Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO "users"("id","name","status") VALUES (?,?,?)`, 1, "alice", "active"); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	writePipelineAcceptanceFile(t, root, "acceptance/active_test.go", fmt.Sprintf(`package acceptance_test
import (
 "bytes"; "context"; "encoding/json"; "net/http"; "net/http/httptest"; "sync"; "testing"
 "example.test/activep5/app"; "github.com/eleven-am/golem/go/golem"; "github.com/jmoiron/sqlx"
)
type payload struct { Data map[string]any `+"`json:\"data\"`"+`; Errors []struct { Message string `+"`json:\"message\"`"+`; Path []any `+"`json:\"path\"`"+`; Extensions map[string]any `+"`json:\"extensions\"`"+` } `+"`json:\"errors\"`"+` }
func request(t *testing.T, server *app.GraphQLServer, query string) payload { t.Helper(); body,_:=json.Marshal(map[string]any{"query":query}); recorder:=httptest.NewRecorder(); server.Handler().ServeHTTP(recorder,httptest.NewRequest(http.MethodPost,"/graphql",bytes.NewReader(body))); var value payload; if err:=json.Unmarshal(recorder.Body.Bytes(),&value); err!=nil { t.Fatalf("decode %%s: %%v",recorder.Body.String(),err) }; return value }
func TestActive(t *testing.T) {
 db,err:=sqlx.Open("sqlite",%q); if err!=nil { t.Fatal(err) }
 application,err:=app.Open(context.Background(),app.Config[app.Principal]{DB:db,Provider:golem.SQLite,ResolvePrincipal:func(context.Context,app.Principal)(app.Principal,error){return app.Principal{ID:"p"},nil}}); if err!=nil {t.Fatal(err)}
 var mu sync.Mutex; reports:=0
 server,err:=application.GraphQL(app.GraphQLConfig[app.Principal]{PrincipalFromContext:func(context.Context)(app.Principal,bool){return app.Principal{ID:"p"},true},ReportInternalError:func(context.Context,error){mu.Lock();reports++;mu.Unlock()}}); if err!=nil {t.Fatal(err)}
 nullable:=request(t,server,`+"`query { users { name broken: nullableFailure } }`"+`); if len(nullable.Errors)!=1 || nullable.Errors[0].Extensions["code"]!="INTERNAL_SERVER_ERROR" || len(nullable.Errors[0].Path)!=3 || nullable.Errors[0].Path[0]!="users" || nullable.Errors[0].Path[1]!=float64(0) || nullable.Errors[0].Path[2]!="broken" { t.Fatalf("nullable=%%#v",nullable) }; users:=nullable.Data["users"].([]any); row:=users[0].(map[string]any); if row["name"]!="alice" || row["broken"]!=nil {t.Fatalf("nullable data=%%#v",nullable.Data)}
 nonnull:=request(t,server,`+"`query { users { name broken: nonNullFailure } }`"+`); if len(nonnull.Errors)!=1 || len(nonnull.Errors[0].Path)!=3 || nonnull.Errors[0].Path[2]!="broken" || nonnull.Data!=nil { t.Fatalf("nonnull=%%#v",nonnull) }
 invalid:=request(t,server,`+"`query { users { name broken: invalidUTF8 } }`"+`); if len(invalid.Errors)!=1 || invalid.Errors[0].Path[2]!="broken" || invalid.Data["users"].([]any)[0].(map[string]any)["name"]!="alice" { t.Fatalf("invalid utf8=%%#v",invalid) }
	 invalidList:=request(t,server,`+"`query { users { name broken: invalidList } }`"+`); if len(invalidList.Errors)!=1 || len(invalidList.Errors[0].Path)!=4 || invalidList.Errors[0].Path[2]!="broken" || invalidList.Errors[0].Path[3]!=float64(1) {t.Fatalf("invalid list=%%#v",invalidList)}; list:=invalidList.Data["users"].([]any)[0].(map[string]any)["broken"].([]any); if list[0]!="ok" || list[1]!=nil || list[2]!="still" {t.Fatalf("invalid list data=%%#v",invalidList.Data)}
	 invalidStrictList:=request(t,server,`+"`query { users { name broken: invalidStrictList } }`"+`); if len(invalidStrictList.Errors)!=1 || len(invalidStrictList.Errors[0].Path)!=4 || invalidStrictList.Errors[0].Path[2]!="broken" || invalidStrictList.Errors[0].Path[3]!=float64(1) || invalidStrictList.Data!=nil {t.Fatalf("invalid strict list=%%#v",invalidStrictList)}
	 invalidNullableStrictList:=request(t,server,`+"`query { users { name broken: invalidNullableStrictList } }`"+`); if len(invalidNullableStrictList.Errors)!=1 || len(invalidNullableStrictList.Errors[0].Path)!=4 || invalidNullableStrictList.Errors[0].Path[2]!="broken" || invalidNullableStrictList.Errors[0].Path[3]!=float64(1) {t.Fatalf("invalid nullable strict list=%%#v",invalidNullableStrictList)}; nullableStrictRow:=invalidNullableStrictList.Data["users"].([]any)[0].(map[string]any); if nullableStrictRow["name"]!="alice" || nullableStrictRow["broken"]!=nil {t.Fatalf("invalid nullable strict list data=%%#v",invalidNullableStrictList.Data)}
	 nullableMutation:=request(t,server,`+"`mutation { first: createUser(data: { id: \"2\", name: \"bob\", status: ACTIVE }) { id broken: nullableFailure } second: updateUser(where: { ID: \"2\" }, data: { name: { set: \"after\" } }) { name } }`"+`); if len(nullableMutation.Errors)!=1 || len(nullableMutation.Errors[0].Path)!=2 || nullableMutation.Errors[0].Path[0]!="first" || nullableMutation.Errors[0].Path[1]!="broken" || nullableMutation.Data["second"].(map[string]any)["name"]!="after" {t.Fatalf("nullable mutation=%%#v",nullableMutation)}
	 nonNullMutation:=request(t,server,`+"`mutation { first: createUser(data: { id: \"3\", name: \"committed\", status: ACTIVE }) { id broken: nonNullFailure } second: updateUser(where: { ID: \"1\" }, data: { name: { set: \"must-not-run\" } }) { name } }`"+`); if len(nonNullMutation.Errors)!=1 || len(nonNullMutation.Errors[0].Path)!=2 || nonNullMutation.Errors[0].Path[0]!="first" || nonNullMutation.Errors[0].Path[1]!="broken" || nonNullMutation.Data!=nil {t.Fatalf("non-null mutation=%%#v",nonNullMutation)}; var original,committed string; if err:=db.Get(&original,`+"`SELECT name FROM users WHERE id=1`"+`);err!=nil{t.Fatal(err)};if err:=db.Get(&committed,`+"`SELECT name FROM users WHERE id=3`"+`);err!=nil{t.Fatal(err)};if original!="alice"||committed!="committed"{t.Fatalf("mutation order original=%%q committed=%%q",original,committed)}
	 nullableCustom:=request(t,server,`+"`mutation { first: nullableMutationFailure second: updateUser(where: { ID: \"1\" }, data: { name: { set: \"nullable-continued\" } }) { name } }`"+`); if len(nullableCustom.Errors)!=1 || nullableCustom.Data["first"]!=nil || nullableCustom.Data["second"].(map[string]any)["name"]!="nullable-continued" || app.NullableMutationCalls!=1 {t.Fatalf("nullable custom=%%#v calls=%%d",nullableCustom,app.NullableMutationCalls)}
	 nonNullCustom:=request(t,server,`+"`mutation { first: nonNullMutationFailure second: updateUser(where: { ID: \"1\" }, data: { name: { set: \"custom-must-not-run\" } }) { name } }`"+`); if len(nonNullCustom.Errors)!=1 || nonNullCustom.Data!=nil || app.NonNullMutationCalls!=1 {t.Fatalf("non-null custom=%%#v calls=%%d",nonNullCustom,app.NonNullMutationCalls)}; if err:=db.Get(&original,`+"`SELECT name FROM users WHERE id=1`"+`);err!=nil{t.Fatal(err)};if original!="nullable-continued"{t.Fatalf("non-null custom executed later root: %%q",original)}
	 exact:=request(t,server,`+"`query { stringValues intValues bigValues decimalValues nestedValues statusValues echoStatus(status: ACTIVE) nullableValues nestedNullableValues maybeString jsonValue users { computedStatus } }`"+`); if len(exact.Errors)!=0 || exact.Data["bigValues"].([]any)[0]!="9007199254740993" || exact.Data["decimalValues"].([]any)[0]!="123.45" || exact.Data["echoStatus"]!="ACTIVE" || exact.Data["nullableValues"].([]any)[1]!=nil || exact.Data["maybeString"]!="maybe" || exact.Data["users"].([]any)[0].(map[string]any)["computedStatus"]!="ACTIVE" { t.Fatalf("exact=%%#v",exact) }
 forged:=request(t,server,`+"`query { invalidStatus }`"+`); if len(forged.Errors)!=1 || forged.Errors[0].Extensions["code"]!="INTERNAL_SERVER_ERROR" {t.Fatalf("forged=%%#v",forged)}
 custom:=request(t,server,`+"`query { failCustom }`"+`); if len(custom.Errors)!=1 || custom.Errors[0].Extensions["code"]!="INTERNAL_SERVER_ERROR" || len(custom.Errors[0].Path)!=1 || custom.Errors[0].Path[0]!="failCustom" {t.Fatalf("custom=%%#v",custom)}
 invalidTime:=request(t,server,`+"`query { invalidDateTime }`"+`); if len(invalidTime.Errors)!=1 || invalidTime.Errors[0].Extensions["code"]!="INTERNAL_SERVER_ERROR" {t.Fatalf("invalid time=%%#v",invalidTime)}
	 mu.Lock(); beforeProvider:=reports; mu.Unlock(); if beforeProvider!=13 {t.Fatalf("trusted reports=%%d want 13",beforeProvider)}
	if err:=db.Close();err!=nil{t.Fatal(err)}; provider:=request(t,server,`+"`query { users { name } }`"+`); if len(provider.Errors)!=1 || provider.Errors[0].Extensions["code"]!="BAD_USER_INPUT" || len(provider.Errors[0].Path)!=1 || provider.Errors[0].Path[0]!="users" {t.Fatalf("provider=%%#v",provider)}
	 mu.Lock(); defer mu.Unlock(); if reports!=13 {t.Fatalf("trusted reports=%%d want 13",reports)}
}
`, "file:"+databasePath+"?_pragma=foreign_keys(1)"))
	command := exec.Command("go", "test", "-mod=mod", "./...")
	command.Dir, command.Env = root, append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh active P5 consumer failed: %v\n%s\nSDL:\n%s", err, output, generatedSDL)
	}
}
