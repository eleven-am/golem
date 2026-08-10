package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/physical"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestFreshGeneratedGQLGenHTTPExactScalarAndRenamedEnumContract(t *testing.T) {
	if strings.Contains(p5ScalarConsumerTest, "github.com/eleven-am/golem/go/internal/") {
		t.Fatal("external exact-scalar consumer imports a Golem internal package")
	}
	root := t.TempDir()
	writePipelineAcceptanceFile(t, root, "go.mod", fmt.Sprintf(`module example.com/scalarapp

go 1.25

require (
	github.com/99designs/gqlgen v0.17.70
	github.com/eleven-am/golem/go v0.0.0
	github.com/vektah/gqlparser/v2 v2.5.23
)

replace github.com/eleven-am/golem/go => %s
`, moduleRoot(t)))
	writePipelineAcceptanceFile(t, root, "tools.go", `//go:build tools

package tools

import (
	_ "github.com/99designs/gqlgen/graphql"
	_ "github.com/vektah/gqlparser/v2/ast"
)
`)
	writePipelineAcceptanceFile(t, root, "actor/actor.go", "package actor\ntype Actor struct{}\n")
	modelSource := strings.ReplaceAll(`package models

import (
  "time"
  "example.com/scalarapp/actor"
  "github.com/eleven-am/golem/go/golem"
)

type Lifecycle string

const (
  LifecycleReady Lifecycle = "ready-wire"
  LifecyclePaused Lifecycle = "paused-wire"
)

func (Lifecycle) GolemEnum() golem.EnumSpec[Lifecycle] {
  return golem.DefineEnum(
    golem.EnumValue(LifecycleReady, golem.GraphQLValue("READY_PUBLIC")),
    golem.EnumValue(LifecyclePaused, golem.GraphQLValue("PAUSED_PUBLIC")),
  )
}

type ScalarRecord struct {
  _ struct{} §golem:"model;id=scalar.Record;table=scalar_records"§
  ID golem.UUID §db:"id" golem:"pk"§
  BigValue int64 §db:"big_value"§
  DecimalValue golem.Decimal §db:"decimal_value" golem:"type=decimal(18,6)"§
  UUIDValue golem.UUID §db:"uuid_value"§
  DateValue golem.Date §db:"date_value"§
  TimeValue golem.Time §db:"time_value"§
  DateTimeValue time.Time §db:"datetime_value"§
  BytesValue []byte §db:"bytes_value"§
  JSONValue golem.JSON[any] §db:"json_value" golem:"type=json"§
  FloatValue float64 §db:"float_value"§
  BooleanValue bool §db:"boolean_value"§
  IntValue int32 §db:"int_value"§
  StringValue string §db:"string_value"§
  Labels golem.List[string] §db:"labels"§
  State Lifecycle §db:"state" golem:"graphql=lifecycle"§
}

func (ScalarRecord) DefinePolicy(rules *golem.Rules[ScalarRecord], _ actor.Actor) {
  rules.CanRead(golem.All[ScalarRecord]())
  rules.CanCreate(golem.All[ScalarRecord]())
  rules.CanUpdate(golem.All[ScalarRecord]())
  rules.CanDelete(golem.All[ScalarRecord]())
}
`, "§", "`")
	writePipelineAcceptanceFile(t, root, "models/models.go", modelSource)
	writePipelineAcceptanceFile(t, root, "schema/schema.go", `package schema

import (
  "example.com/scalarapp/actor"
  "example.com/scalarapp/models"
  "github.com/eleven-am/golem/go/golem"
)

func DefineSchema(schema *golem.Schema) {
  golem.SchemaName(schema, "scalar_http_acceptance")
  golem.Actor[actor.Actor](schema)
  golem.Model[models.ScalarRecord](schema)
  golem.Providers(schema, golem.SQLite)
}
`)
	writePipelineAcceptanceFile(t, root, "app/doc.go", "package app\n")

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	tidy.Env = append(os.Environ(), "GOWORK=off")
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("prepare exact-scalar module: %v\n%s", err, output)
	}
	request := Request{
		Compile:    compile.Config{Dir: root, Pattern: "./schema", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.com/scalarapp/app", PackageName: "app", Directory: filepath.Join(root, "app")},
		Lowerers:   []physical.Lowerer{sqliteprovider.New()},
		Env:        []string{"GOWORK=off"},
	}
	reviewed := p8BuildWithReviewedSQLiteHistory(t, context.Background(), request)
	first := reviewed.Result
	writeP5ScalarGeneratedArtifacts(t, root, first.Prospective.Artifacts)
	request.ReviewedMigrations = []ReviewedMigration{reviewed.History}
	second, err := Build(context.Background(), request)
	if err != nil {
		t.Fatalf("regenerate exact-scalar module: %v", err)
	}
	assertP5ScalarArtifactsDeterministic(t, first.Prospective.Artifacts, second.Prospective.Artifacts)
	if len(first.Providers) != 1 {
		t.Fatalf("providers=%d want=1", len(first.Providers))
	}

	databasePath := filepath.Join(t.TempDir(), "scalar-http.db")
	database, _, err := sqliteprovider.New().Open(context.Background(), "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.New().ApplyMigration(context.Background(), database, reviewed.History.Manifest, reviewed.History.Files); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	consumerDSN := "file:" + databasePath
	writePipelineAcceptanceFile(t, root, "acceptance/scalar_http_test.go", fmt.Sprintf(p5ScalarConsumerTest, consumerDSN))
	command := exec.Command("go", "test", "-mod=mod", "-count=1", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh exact-scalar gqlgen consumer failed: %v\n%s", err, output)
	}
}

func writeP5ScalarGeneratedArtifacts(t *testing.T, root string, artifacts []manifest.Artifact) {
	t.Helper()
	for _, artifact := range artifacts {
		switch artifact.Kind {
		case manifest.ArtifactModelGo, manifest.ArtifactBindingsGo, manifest.ArtifactRegistryGo, manifest.ArtifactGraphQLGo, manifest.ArtifactGraphQLSDL:
			writePipelineAcceptanceFile(t, root, artifact.Path, string(artifact.Content))
		}
	}
}

func assertP5ScalarArtifactsDeterministic(t *testing.T, first, second []manifest.Artifact) {
	t.Helper()
	type generated struct {
		path string
		body []byte
	}
	collect := func(artifacts []manifest.Artifact) []generated {
		values := make([]generated, 0, len(artifacts))
		for _, artifact := range artifacts {
			values = append(values, generated{path: artifact.Path, body: artifact.Content})
		}
		sort.Slice(values, func(i, j int) bool { return values[i].path < values[j].path })
		return values
	}
	left, right := collect(first), collect(second)
	if len(left) != len(right) {
		t.Fatalf("regenerated artifact count=%d want=%d", len(right), len(left))
	}
	for index := range left {
		if left[index].path != right[index].path || !bytes.Equal(left[index].body, right[index].body) {
			t.Fatalf("regenerated artifact drift at %q versus %q", left[index].path, right[index].path)
		}
	}
}

const p5ScalarConsumerTest = `package acceptance_test

import (
  "context"
  "encoding/json"
  "fmt"
  "io"
  "net/http"
  "net/http/httptest"
  "strings"
  "testing"

  "example.com/scalarapp/actor"
  "example.com/scalarapp/app"
  providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestGeneratedGQLGenHTTPExactScalarAndRenamedEnumContract(t *testing.T) {
  database, err := providersqlite.Open(context.Background(), providersqlite.Config{DataSourceName:%q}); if err != nil { t.Fatal(err) }
  t.Cleanup(func(){ _ = database.Close() })
  application, err := app.Open(context.Background(), app.Config[string]{Database:database, ResolvePrincipal:func(context.Context,string)(actor.Actor,error){ return actor.Actor{},nil }})
  if err != nil { t.Fatal(err) }
  server, err := application.GraphQL(app.GraphQLConfig[string]{PrincipalFromContext:func(context.Context)(string,bool){ return "principal",true }, ReportInternalError:func(_ context.Context, err error){ t.Errorf("trusted GraphQL error: %%v",err) }})
  if err != nil { t.Fatal(err) }
  if server.Handler() == nil || !strings.Contains(server.SDL(), "scalar BigInt") || !strings.Contains(server.SDL(), "READY_PUBLIC") || strings.Contains(server.SDL(), "ready-wire") { t.Fatalf("generated gqlgen server/SDL incomplete") }

  selection := "id bigValue decimalValue uuidValue dateValue timeValue dateTimeValue bytesValue jsonValue floatValue booleanValue intValue stringValue labels lifecycle"
  createQuery := "mutation Create($data: ScalarRecordCreateInput!) { createScalarRecord(data: $data) { "+selection+" } }"
  values := exactValues()
  created := request(t, server.Handler(), createQuery, map[string]any{"data":values})
  want := exactRecord()
  if got := object(t, object(t, created["data"], "data")["createScalarRecord"], "data.createScalarRecord"); !deepJSONEqual(got, want) { t.Fatalf("create wire result=%%s want=%%s", mustJSON(got), mustJSON(want)) }
  rawCreated := mustJSON(created)
  if !strings.Contains(rawCreated, ` + "`" + `"bigValue":"9007199254740993"` + "`" + `) || !strings.Contains(rawCreated, ` + "`" + `"decimalValue":"123456789012.345678"` + "`" + `) || !strings.Contains(rawCreated, ` + "`" + `"n":9007199254740993123456789e1` + "`" + `) { t.Fatalf("exact numbers lost on wire: %%s", rawCreated) }

  where := map[string]any{
    "bigValue":map[string]any{"equals":"9007199254740993"}, "decimalValue":map[string]any{"equals":"123456789012.345678"},
    "uuidValue":map[string]any{"equals":"123e4567-e89b-12d3-a456-426614174000"}, "dateValue":map[string]any{"equals":"2024-02-29"},
    "timeValue":map[string]any{"equals":"23:59:58.1234"}, "dateTimeValue":map[string]any{"equals":"2024-02-29T12:34:56.123456+02:00"},
    "bytesValue":map[string]any{"equals":"AAEC/w=="}, "jsonValue":map[string]any{"equals":json.RawMessage(` + "`" + `{"n":90071992547409931234567890,"nested":{"v":1.25}}` + "`" + `)},
    "floatValue":map[string]any{"equals":1.25}, "booleanValue":map[string]any{"equals":true}, "intValue":map[string]any{"equals":int32(2147483647)},
    "stringValue":map[string]any{"equals":"hello"}, "labels":map[string]any{"hasEvery":[]string{"alpha","beta"},"has":"beta"}, "lifecycle":map[string]any{"equals":"READY_PUBLIC"},
  }
  found := request(t, server.Handler(), "query Find($where: ScalarRecordWhereInput!) { scalarRecords(where: $where, take: 1) { "+selection+" } }", map[string]any{"where":where})
  rows, ok := object(t, found["data"], "data")["scalarRecords"].([]any); if !ok || len(rows) != 1 { t.Fatalf("filtered rows=%%#v", found) }
  if got := object(t, rows[0], "data.scalarRecords[0]"); !deepJSONEqual(got, want) { t.Fatalf("read wire result=%%s want=%%s", mustJSON(got), mustJSON(want)) }

  updated := request(t, server.Handler(), "mutation Update($where: ScalarRecordWhereUniqueInput!, $data: ScalarRecordUpdateInput!) { updateScalarRecord(where: $where, data: $data) { labels } }", map[string]any{"where":map[string]any{"ID":"00000000-0000-0000-0000-000000000001"},"data":map[string]any{"labels":map[string]any{"set":[]string{"gamma","delta"}}}})
  if updated["data"] == nil { t.Fatalf("scalar-list update response=%%s", mustJSON(updated)) }
  if got := object(t, object(t, updated["data"], "data")["updateScalarRecord"], "data.updateScalarRecord")["labels"]; !deepJSONEqual(got, []any{"gamma","delta"}) { t.Fatalf("updated scalar list=%%s", mustJSON(got)) }

  invalid := []struct{name, field string; value any}{
    {"bigint-leading-zero","bigValue","01"}, {"decimal-trailing-zero","decimalValue","1.230"},
    {"uuid-uppercase","uuidValue","123E4567-E89B-12D3-A456-426614174000"}, {"date-noncanonical","dateValue","2024-2-29"},
    {"time-overprecision","timeValue","23:59:58.1234000"}, {"datetime-overprecision","dateTimeValue","2024-02-29T10:34:56.1234567Z"},
    {"bytes-unpadded","bytesValue","AAEC/w"}, {"enum-wire-instead-of-graphql","lifecycle","ready-wire"},
  }
  for index, test := range invalid { t.Run(test.name, func(t *testing.T){
    candidate := exactValues(); candidate["id"] = fmt.Sprintf("00000000-0000-0000-0000-%%012d", index+2); candidate[test.field] = test.value
    response := request(t, server.Handler(), createQuery, map[string]any{"data":candidate})
    errors, ok := response["errors"].([]any); if !ok || len(errors) == 0 { t.Fatalf("invalid input accepted: %%s",mustJSON(response)) }
    first := object(t, errors[0], "errors[0]"); extensions := object(t, first["extensions"], "errors[0].extensions")
    if extensions["code"] != "BAD_USER_INPUT" { t.Fatalf("invalid input classification=%%s",mustJSON(response)) }
    var count int; if err := database.UnsafeSQLX().GetContext(context.Background(), &count, "SELECT COUNT(*) FROM scalar_records WHERE id = ?", candidate["id"]); err != nil { t.Fatal(err) }
    if count != 0 { t.Fatalf("invalid input persisted %%d rows",count) }
  }) }
}

func exactValues() map[string]any { return map[string]any{
  "id":"00000000-0000-0000-0000-000000000001", "bigValue":"9007199254740993", "decimalValue":"123456789012.345678",
  "uuidValue":"123e4567-e89b-12d3-a456-426614174000", "dateValue":"2024-02-29", "timeValue":"23:59:58.1234",
  "dateTimeValue":"2024-02-29T12:34:56.123456+02:00", "bytesValue":"AAEC/w==",
  "jsonValue":json.RawMessage(` + "`" + `{"n":90071992547409931234567890,"nested":{"v":1.25}}` + "`" + `), "floatValue":1.25,
  "booleanValue":true, "intValue":int32(2147483647), "stringValue":"hello", "labels":[]string{"alpha","beta"}, "lifecycle":"READY_PUBLIC",
} }
func exactRecord() map[string]any { value := exactValues(); value["dateTimeValue"] = "2024-02-29T10:34:56.123456Z"; var decoded any; if err := decodeJSON([]byte(` + "`" + `{"n":9007199254740993123456789e1,"nested":{"v":125e-2}}` + "`" + `), &decoded); err != nil { panic(err) }; value["jsonValue"] = decoded; return value }

func request(t *testing.T, handler http.Handler, query string, variables map[string]any) map[string]any { t.Helper(); payload, err := json.Marshal(map[string]any{"query":query,"variables":variables}); if err != nil { t.Fatal(err) }; recorder := httptest.NewRecorder(); req := httptest.NewRequest(http.MethodPost,"/graphql",strings.NewReader(string(payload))); req.Header.Set("Content-Type","application/json"); handler.ServeHTTP(recorder,req); body, err := io.ReadAll(recorder.Result().Body); if err != nil { t.Fatal(err) }; if recorder.Code != http.StatusOK { t.Fatalf("HTTP status=%%d body=%%s",recorder.Code,body) }; var response map[string]any; if err := decodeJSON(body,&response); err != nil { t.Fatalf("decode response: %%v body=%%s",err,body) }; return response }
func decodeJSON(input []byte, output any) error { decoder := json.NewDecoder(strings.NewReader(string(input))); decoder.UseNumber(); return decoder.Decode(output) }
func object(t *testing.T, value any, path string) map[string]any { t.Helper(); result, ok := value.(map[string]any); if !ok { t.Fatalf("%%s=%%#v want object",path,value) }; return result }
func deepJSONEqual(left,right any) bool { return mustJSON(left)==mustJSON(right) }
func mustJSON(value any) string { encoded, err := json.Marshal(value); if err != nil { panic(err) }; return string(encoded) }
`
