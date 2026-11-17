package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresqlprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func TestFreshGeneratedHomeFeedDTOHTTP(t *testing.T) {
	runGeneratedDTOConsumer(t, "objecttypes", func(modelSource string) string {
		modelSource = strings.Replace(modelSource, `"context"`, "\"context\"\n\"sync/atomic\"", 1)
		modelSource = strings.Replace(modelSource, "var seed int32", "HomeFeedCalls.Add(1)\nvar seed int32", 1)
		modelSource = strings.Replace(modelSource, "_ = seed", "HomeFeedSeed.Store(seed)", 1)
		return modelSource + "\nvar HomeFeedCalls atomic.Int32\nvar HomeFeedSeed atomic.Int32\n"
	}, homeFeedDTOConsumerTest)
}

func runGeneratedDTOConsumer(t *testing.T, fixture string, rewrite func(string) string, consumer string) {
	t.Helper()
	runGeneratedDTOConsumerWithProviders(t, fixture, rewrite, consumer, compilerir.SQLite)
}

// Generate and compile one external-style consumer, then run that exact binary
// against each requested provider. The consumer owns its application lifecycle;
// this helper owns only reviewed physical provisioning and child execution.
type generatedDTOArtifacts struct {
	Directory, Writer3, Writer4, DSN string
	Environment                      []string
	Provider                         compilerir.Provider
	Result                           Result
}

type generatedDTOConsumerModulePin struct {
	Path, Version string
}

type generatedDTOExecution struct {
	consumerModulePins []generatedDTOConsumerModulePin
	coordinate         func(*testing.T, generatedDTOArtifacts)
	writer4Only        bool
	sqliteDirectory    string
}

func runGeneratedDTOConsumerWithProviders(t *testing.T, fixture string, rewrite func(string) string, consumer string, providers ...compilerir.Provider) {
	runGeneratedDTOConsumerArtifacts(t, fixture, rewrite, consumer, generatedDTOExecution{}, providers...)
}

// Coordinated acceptances compile only the writer artifacts they execute.
// Ordinary callers keep the existing single-artifact execution path.
func runGeneratedDTOConsumerArtifacts(t *testing.T, fixture string, rewrite func(string) string, consumer string, execution generatedDTOExecution, providers ...compilerir.Provider) {
	t.Helper()
	if execution.writer4Only && execution.coordinate == nil {
		t.Fatal("writer4-only execution requires a coordinator")
	}
	if len(providers) == 0 {
		t.Fatal("generated consumer requires at least one provider")
	}
	var lowerers []physical.Lowerer
	selected := make(map[compilerir.Provider]bool, len(providers))
	for _, provider := range providers {
		if selected[provider] {
			t.Fatalf("duplicate generated consumer provider %q", provider)
		}
		selected[provider] = true
		switch provider {
		case compilerir.SQLite:
			lowerers = append(lowerers, sqliteprovider.New())
		case compilerir.PostgreSQL:
			lowerers = append(lowerers, postgresqlprovider.New())
		default:
			t.Fatalf("unsupported generated consumer provider %q", provider)
		}
	}
	if execution.sqliteDirectory != "" {
		if !selected[compilerir.SQLite] || execution.coordinate == nil {
			t.Fatal("an external SQLite directory requires an explicit SQLite characterization coordinator")
		}
		if err := validateCharacterizationSQLiteDirectory(execution.sqliteDirectory); err != nil {
			t.Fatal(err)
		}
	}
	pgRoot, pgDSN := os.Getenv("GOLEM_POLICY_HISTORY_PG_ROOT"), os.Getenv("GOLEM_POLICY_HISTORY_PG_DSN")
	if selected[compilerir.PostgreSQL] && (pgRoot == "") != (pgDSN == "") {
		t.Fatal("generated PostgreSQL consumer requires both private cluster root and DSN")
	}
	buildEnv := []string{"GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOMAXPROCS=1"}
	environment := append(os.Environ(), buildEnv...)
	if execution.coordinate != nil {
		environment = append(environment, "GOFLAGS=")
	}
	root := t.TempDir()
	writePipelineAcceptanceFile(t, root, "go.mod", fmt.Sprintf(`module example.test/home

go 1.26

require github.com/eleven-am/golem/go v0.0.0

replace github.com/eleven-am/golem/go => %s
`, moduleRoot(t)))
	source, err := os.ReadFile(filepath.Join("../../compiler/compile/testdata", fixture, "schema.go"))
	if err != nil {
		t.Fatal(err)
	}
	modelSource := strings.Replace(string(source), "package "+fixture, "package app", 1)
	if rewrite != nil {
		modelSource = rewrite(modelSource)
	}
	writePipelineAcceptanceFile(t, root, "schema.go", modelSource)
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir, tidy.Env = root, environment
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("prepare generated consumer module: %v\n%s", err, output)
	}
	ctx := context.Background()
	request := Request{
		Compile:    compile.Config{Dir: root, Pattern: "."},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.test/home", PackageName: "app", Directory: root},
		Lowerers:   lowerers, Env: buildEnv,
	}
	// Reviewed histories affect emission, not source analysis. Reuse the
	// existing analysis/cache owner instead of loading the schema graph twice.
	analysis, err := Analyze(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := analysis.Build(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Providers) != len(providers) {
		t.Fatalf("generated consumer providers=%d want=%d", len(initial.Providers), len(providers))
	}
	requiresPGVector := false
	histories := make(map[compilerir.Provider]ReviewedMigration, len(providers))
	for _, result := range initial.Providers {
		provider := result.Provider.Provider
		if !selected[provider] {
			t.Fatalf("unexpected generated consumer provider %q", provider)
		}
		var history ReviewedMigration
		switch provider {
		case compilerir.SQLite:
			history = p8InitialSQLiteHistory(t, initial.ModelFingerprint, result.Schema)
		case compilerir.PostgreSQL:
			for _, extension := range result.Schema.Extensions {
				requiresPGVector = requiresPGVector || extension.Kind == semanticcontract.IndexKind
			}
			history = initialPostgreSQLSemanticHistory(t, initial.ModelFingerprint, result.Schema)
		}
		histories[provider] = history
		request.ReviewedMigrations = append(request.ReviewedMigrations, history)
	}
	bound, err := analysis.Build(ctx, request.ReviewedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	writeP5ScalarGeneratedArtifacts(t, root, bound.Prospective.Artifacts)
	writePipelineAcceptanceFile(t, root, "acceptance/dto_test.go", consumer)
	// Consumer-only pins are applied after the schema-only tidy, which otherwise
	// removes dependencies used solely by acceptance/dto_test.go.
	for _, pin := range execution.consumerModulePins {
		if pin.Path == "" || pin.Version == "" || strings.ContainsAny(pin.Path+pin.Version, " \t\r\n@") {
			t.Fatal("invalid generated consumer module pin")
		}
		pinCommand := exec.Command("go", "mod", "edit", "-require="+pin.Path+"@"+pin.Version)
		pinCommand.Dir, pinCommand.Env = root, environment
		if output, err := pinCommand.CombinedOutput(); err != nil {
			t.Fatalf("pin generated consumer module: %v\n%s", err, output)
		}
	}
	executable := filepath.Join(root, "dto-consumer.test")
	if execution.writer4Only {
		executable = ""
	} else {
		compileConsumer := exec.Command("go", "test", "-c", "-mod=mod", "-p=1", "-o", executable, "./acceptance")
		compileConsumer.Dir, compileConsumer.Env = root, environment
		if output, err := compileConsumer.CombinedOutput(); err != nil {
			t.Fatalf("compile generated consumer: %v\n%s", err, output)
		}
	}
	candidate := ""
	for _, pin := range execution.consumerModulePins {
		selected := exec.Command("go", "list", "-m", "-f", "{{.Version}}{{if .Replace}} replaced{{end}}", pin.Path)
		selected.Dir, selected.Env = root, environment
		output, err := selected.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != pin.Version {
			t.Fatalf("generated consumer module %s did not select exact unreplaced %s: %v %s", pin.Path, pin.Version, err, output)
		}
	}
	if execution.coordinate != nil {
		sourcePath := filepath.Join(moduleRoot(t), "runtime", "mutation_history_finalize.go")
		source, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		const old = "const mutationHistoryWriterVersion = mutationfact.FormatVersion"
		const next = "const mutationHistoryWriterVersion = mutationfact.HistoryFactVersion"
		if strings.Count(string(source), old) != 1 {
			t.Fatal("writer3 release seam changed")
		}
		writerPath := filepath.Join(t.TempDir(), "writer4.go")
		if err := os.WriteFile(writerPath, []byte(strings.Replace(string(source), old, next, 1)), 0600); err != nil {
			t.Fatal(err)
		}
		overlay, err := json.Marshal(struct{ Replace map[string]string }{map[string]string{sourcePath: writerPath}})
		if err != nil {
			t.Fatal(err)
		}
		overlayPath := filepath.Join(root, "writer4-overlay.json")
		if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
			t.Fatal(err)
		}
		candidate = filepath.Join(root, "dto-writer4.test")
		command := exec.Command("go", "test", "-c", "-mod=mod", "-p=1", "-overlay", overlayPath, "-o", candidate, "./acceptance")
		command.Dir, command.Env = root, environment
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("compile writer4 consumer: %v\n%s", err, output)
		}
	}
	for _, provider := range providers {
		t.Run(string(provider), func(t *testing.T) {
			history := histories[provider]
			provision := func() string {
				var dsn string
				switch provider {
				case compilerir.SQLite:
					var durability sqliteprovider.DurabilityProfile
					dsn, durability = characterizationSQLiteLocation(t, execution.sqliteDirectory)
					database, _, err := sqliteprovider.New().OpenWithDurability(ctx, dsn, durability)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = database.Close() })
					p8ApplyReviewedSQLiteHistory(t, ctx, database, history)
					if err := database.Close(); err != nil {
						t.Fatal(err)
					}
				case compilerir.PostgreSQL:
					if requiresPGVector {
						// Semantic consumers use the existing explicit pgvector profile;
						// ordinary consumers retain their private native-cluster owner.
						dsn = testenv.DisposablePGVector(t)
					} else {
						if pgRoot == "" && pgDSN == "" {
							testenv.SkipMissingPostgreSQL(t, "requires explicit GOLEM_POLICY_HISTORY_PG_ROOT and GOLEM_POLICY_HISTORY_PG_DSN")
						}
						dsn = testenv.DisposablePostgreSQLInRoot(t, pgRoot, pgDSN)
					}
					engine := postgresqlprovider.New()
					database, _, err := engine.Open(ctx, dsn)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = database.Close() })
					if err := engine.ApplyMigration(ctx, database, history.Manifest, history.Files); err != nil {
						t.Fatal(err)
					}
					if err := database.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return dsn
			}
			dsn := provision()
			if execution.coordinate != nil {
				execution.coordinate(t, generatedDTOArtifacts{Directory: root, Writer3: executable, Writer4: candidate, DSN: dsn, Environment: append([]string(nil), environment...), Provider: provider, Result: bound})
				return
			}
			alternate := ""
			if fixture == "continuationpages" {
				// Identical reviewed artifacts must initialize independent identities.
				alternate = provision()
			}
			runContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			command := exec.CommandContext(runContext, executable, "-test.v", "-test.count=1", "-test.timeout=2m")
			command.Dir = root
			command.Env = append(append([]string(nil), environment...), "GOLEM_OBJECT_PROVIDER="+string(provider), "GOLEM_OBJECT_DB="+dsn, "GOLEM_OBJECT_ALTERNATE_DB="+alternate)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("generated %s consumer failed: %v\n%s", provider, err, output)
			}
		})
	}
}

const homeFeedDTOConsumerTest = `package acceptance_test

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "os"
 "strings"
 "testing"
 app "example.test/home"
 golemgraphql "github.com/eleven-am/golem/go/graphql"
 providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestHomeFeed(t *testing.T) {
 database, err := providersqlite.Open(context.Background(), providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
 if err != nil { t.Fatal(err) }; t.Cleanup(func(){ _ = database.Close() })
 application, err := app.Open(context.Background(), app.Config[app.Principal]{Database: database, ResolvePrincipal: func(context.Context, app.Principal)(app.Actor,error){ return app.Actor{},nil }})
 if err != nil { t.Fatal(err) }
 config := app.GraphQLConfig[app.Principal]{PrincipalFromContext: func(context.Context)(app.Principal,bool){ return app.Principal{},true }, ReportInternalError: func(_ context.Context, err error){ t.Errorf("internal GraphQL failure: %v",err) }}
 server, err := application.GraphQL(config); if err != nil { t.Fatal(err) }
 if _,err:=application.System().Users.Create(context.Background(),app.Users.Create(app.Users.Name.Create("viewer")));err!=nil{t.Fatal(err)}
 computed:=request(t,server.Handler(),"{ users { metadata { id _version } } }",nil)
 if computed["errors"]!=nil{t.Fatalf("computed-only DTO: %#v",computed)}
 users:=computed["data"].(map[string]any)["users"].([]any)
 if len(users)!=1{t.Fatalf("computed-only parent identity: %#v",computed)}
 computedMetadata:=users[0].(map[string]any)["metadata"].(map[string]any)
 if computedMetadata["id"]!="opaque-id" || computedMetadata["_version"]!="9007199254740993"{t.Fatalf("computed DTO metadata names: %#v",computed)}
 query := "query Home($input: HomeInput!, $show: Boolean!) { feed: homeFeed(input: $input) { __typename page totalPages totalResults exhausted results { ...Segment label: kind index @include(if: $show) } } } fragment Segment on HomeSegment { kind }"
 for _, show := range []bool{true,false} {
  response := request(t, server.Handler(), query, map[string]any{"input": map[string]any{"range": map[string]any{"seed":7}}, "show":show})
  if response["errors"] != nil { t.Fatalf("response=%#v",response) }
  feed := response["data"].(map[string]any)["feed"].(map[string]any)
  if feed["__typename"] != "HomeFeedPage" || feed["page"] != float64(1) || feed["totalPages"] != float64(1) || feed["totalResults"] != float64(1) || feed["exhausted"] != true { t.Fatalf("feed=%#v",feed) }
  results := feed["results"].([]any); if len(results)!=1 { t.Fatalf("results=%#v",results) }; item := results[0].(map[string]any)
  if item["kind"] != "BASIC_PICK" || item["label"] != "BASIC_PICK" { t.Fatalf("enum/alias=%#v",item) }
  index, present := item["index"]; if present != show || show && index != float64(1) { t.Fatalf("directives/one-based segment index=%#v",item) }
 }
 if app.HomeFeedSeed.Load()!=7 { t.Fatal("nested input did not reach the resolver exactly") }
 metadataResponse := request(t,server.Handler(),"{ transportMetadata { id _version maybe { kind } items { kind index } grid { index } } }",nil)
 if metadataResponse["errors"] != nil { t.Fatalf("metadata response=%#v",metadataResponse) }
 for _, version := range []int{1,2,3} {
  negotiated:=request(t,server.Handler(),"{ transportMetadata { id } }",nil,map[string]any{"golem":map[string]any{"protocol":1,"contract":server.ContractFingerprint().String(),"manifestVersion":version}})
  if version==2 {
   if negotiated["errors"]!=nil || negotiated["data"].(map[string]any)["transportMetadata"].(map[string]any)["id"]!="opaque-id" { t.Fatalf("matching manifest refused: %#v",negotiated) }
  } else {
   if negotiated["errors"]==nil || negotiated["errors"].([]any)[0].(map[string]any)["extensions"].(map[string]any)["code"]!="GOLEM_PROTOCOL_MISMATCH" { t.Fatalf("unsupported manifest %d accepted: %#v",version,negotiated) }
  }
 }
 metadata := metadataResponse["data"].(map[string]any)["transportMetadata"].(map[string]any)
 if metadata["id"] != "opaque-id" || metadata["_version"] != "9007199254740993" || metadata["maybe"] != nil { t.Fatalf("DTO metadata/nullable object=%#v",metadata) }
 items := metadata["items"].([]any)
 if len(items)!=2 || items[0]!=nil || items[1].(map[string]any)["index"]!=float64(4) { t.Fatalf("nullable list elements=%#v",items) }
 grid:=metadata["grid"].([]any)
 if len(grid)!=2 || len(grid[1].([]any))!=0 || grid[0].([]any)[0].(map[string]any)["index"]!=float64(4) { t.Fatalf("nested lists=%#v",grid) }
 calls := app.HomeFeedCalls.Load()
 if calls != 2 { t.Fatalf("resolver instrumentation did not observe both valid queries: %d",calls) }
 for _, input := range []any{map[string]any{"range":map[string]any{}}, map[string]any{"unknown":true}, map[string]any{"range":map[string]any{"seed":nil}}} {
  response := request(t,server.Handler(),query,map[string]any{"input":input,"show":true})
  if response["errors"] == nil { t.Fatalf("accepted invalid nested input: %#v",response) }
 }
 config.Limits = golemgraphql.Limits{MaxComplexity: 10}
 limited, err := application.GraphQL(config); if err != nil { t.Fatal(err) }
 response := request(t,limited.Handler(),query,map[string]any{"input":map[string]any{},"show":true})
 if response["errors"] == nil { t.Fatalf("accepted DTO query above cost bound: %#v",response) }
 failure:=response["errors"].([]any)[0].(map[string]any)
 if failure["extensions"].(map[string]any)["code"]!="QUERY_LIMIT_EXCEEDED" { t.Fatalf("wrong query cost failure: %#v",failure) }
 if app.HomeFeedCalls.Load() != calls { t.Fatal("invalid input or query cost reached the resolver") }
 config.Limits = golemgraphql.Limits{MaxComplexity: 2}
 fixed, err := application.GraphQL(config); if err != nil { t.Fatal(err) }
 response = request(t,fixed.Handler(),"{ homeFeedList(take: 1) { page } }",nil)
 if response["errors"] == nil { t.Fatalf("custom take argument reduced its declared result cost: %#v",response) }
 failure=response["errors"].([]any)[0].(map[string]any)
 if failure["extensions"].(map[string]any)["code"]!="QUERY_LIMIT_EXCEEDED" || failure["extensions"].(map[string]any)["cost"]!=float64(3) { t.Fatalf("wrong custom list cost failure: %#v",failure) }
 if app.HomeFeedCalls.Load() != calls { t.Fatal("underestimated custom list cost reached resolver") }
}

func request(t *testing.T, handler http.Handler, query string, variables map[string]any, extensions ...map[string]any) map[string]any {
 t.Helper(); payload:=map[string]any{"query":query,"variables":variables};if len(extensions)>0 { payload["extensions"]=extensions[0] };body,err:=json.Marshal(payload);if err!=nil{t.Fatal(err)}
 recorder:=httptest.NewRecorder(); req:=httptest.NewRequest(http.MethodPost,"/graphql",strings.NewReader(string(body)));req.Header.Set("Content-Type","application/json");handler.ServeHTTP(recorder,req)
 if recorder.Code!=http.StatusOK { t.Fatalf("HTTP status=%d body=%s",recorder.Code,recorder.Body.String()) }
 var response map[string]any;if err:=json.Unmarshal(recorder.Body.Bytes(),&response);err!=nil{t.Fatal(err)};return response
}
`
