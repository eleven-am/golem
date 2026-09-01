package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/physical"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestFreshGeneratedSemanticSQLiteApplicationOwnsEmbeddingLifecycle(t *testing.T) {
	root := t.TempDir()
	writePipelineAcceptanceFile(t, root, "go.mod", fmt.Sprintf(`module example.com/semanticapp

go 1.25

require github.com/eleven-am/golem/go v0.0.0

replace github.com/eleven-am/golem/go => %s
`, moduleRoot(t)))
	writePipelineAcceptanceFile(t, root, "actor/actor.go", "package actor\ntype Actor struct { Private bool; Denied bool }\n")
	modelSource := strings.ReplaceAll(`package models

import (
  "example.com/semanticapp/actor"
  "github.com/eleven-am/golem/go/golem"
)

type Post struct {
  _ struct{} §golem:"model;id=semantic.Post;table=posts"§
  ID golem.UUID §db:"id" golem:"id=semantic.Post.ID;pk"§
  Title string §db:"title"§
  Body string §db:"body"§
}

func (Post) GolemModel() golem.ModelSpec[Post] {
  return golem.DefineModel(golem.SemanticIndex("related", "content", Posts.Title, Posts.Body))
}

func (Post) DefinePolicy(rules *golem.Rules[Post], value actor.Actor) {
  if value.Denied { return }
  if value.Private {
    rules.CanRead(golem.All[Post]())
    return
  }
  rules.CanRead(Posts.Title.StartsWith("public"))
}
`, "§", "`")
	writePipelineAcceptanceFile(t, root, "models/models.go", modelSource)
	writePipelineAcceptanceFile(t, root, "schema/schema.go", `package schema

import (
  "example.com/semanticapp/actor"
  "example.com/semanticapp/models"
  "github.com/eleven-am/golem/go/golem"
)

func DefineSchema(schema *golem.Schema) {
  golem.SchemaName(schema, "semantic_acceptance")
  golem.Actor[actor.Actor](schema)
  golem.Model[models.Post](schema)
  golem.Providers(schema, golem.SQLite)
  golem.EmbeddingSpace(schema, "content", 3)
}
`)
	writePipelineAcceptanceFile(t, root, "app/doc.go", "package app\n")
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	tidy.Env = append(os.Environ(), "GOWORK=off")
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("prepare semantic consumer: %v\n%s", err, output)
	}

	request := Request{
		Compile:    compile.Config{Dir: root, Pattern: "./schema", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.com/semanticapp/app", PackageName: "app", Directory: filepath.Join(root, "app")},
		Lowerers:   []physical.Lowerer{sqliteprovider.New()},
		Env:        []string{"GOWORK=off"},
	}
	reviewed := p8BuildWithReviewedSQLiteHistory(t, context.Background(), request)
	writeP5ScalarGeneratedArtifacts(t, root, reviewed.Result.Prospective.Artifacts)
	databasePath := filepath.Join(t.TempDir(), "semantic-application.db")
	database, _, err := sqliteprovider.New().Open(context.Background(), "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	p8ApplyReviewedSQLiteHistory(t, context.Background(), database, reviewed.History)
	for _, row := range [][3]string{
		{"10000000-0000-0000-0000-000000000001", "public alpha", "shared topic"},
		{"10000000-0000-0000-0000-000000000002", "private alpha", "secret canary"},
		{"10000000-0000-0000-0000-000000000003", "public beta", "other topic"},
	} {
		if _, err := database.Exec(`INSERT INTO "posts" ("id","title","body") VALUES (?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	writePipelineAcceptanceFile(t, root, "acceptance/semantic_test.go", fmt.Sprintf(`package acceptance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
  "strings"
  "sync"
  "testing"
  "time"

  "example.com/semanticapp/actor"
  "example.com/semanticapp/app"
  "example.com/semanticapp/models"
  "github.com/eleven-am/golem/go/embedding"
  "github.com/eleven-am/golem/go/golem"
	golemgraphql "github.com/eleven-am/golem/go/graphql"
  "github.com/eleven-am/golem/go/observe"
  providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
  "github.com/eleven-am/golem/go/queue"
  golemruntime "github.com/eleven-am/golem/go/runtime"
)

type embedder struct {
  specification embedding.Specification
  mu sync.Mutex
  calls int
}

func (value *embedder) Specification() embedding.Specification { return value.specification }
func (value *embedder) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
  value.mu.Lock()
  value.calls += len(inputs)
  value.mu.Unlock()
  result := make([]embedding.Vector, len(inputs))
  for index, input := range inputs {
    vector := []float32{0, 0, 1}
    if strings.Contains(input.Text(), "alpha") { vector = []float32{1, 0, 0} }
    if strings.Contains(input.Text(), "beta") { vector = []float32{0, 1, 0} }
    result[index], _ = embedding.NewVector(vector)
  }
  return result, nil
}
func (value *embedder) count() int { value.mu.Lock(); defer value.mu.Unlock(); return value.calls }

type observed struct {
  operation observe.Operation
  outcome observe.Outcome
  reason observe.Reason
  statements int
  aggregate int64
}

type observationTrace struct {
  mu sync.Mutex
  values []observed
}

func (trace *observationTrace) ObserveGolem(_ context.Context, value observe.Observation) {
  if value.Kind() != observe.KindSemantic { return }
  trace.mu.Lock()
  defer trace.mu.Unlock()
  trace.values = append(trace.values, observed{
    operation: value.Operation(), outcome: value.Outcome(), reason: value.Reason(),
    statements: value.StatementCount(), aggregate: value.AggregateCount(),
  })
}

func (trace *observationTrace) reset() {
  trace.mu.Lock()
  defer trace.mu.Unlock()
  trace.values = nil
}

func (trace *observationTrace) snapshot() []observed {
  trace.mu.Lock()
  defer trace.mu.Unlock()
  return append([]observed(nil), trace.values...)
}

func assertSemanticTrace(t *testing.T, trace *observationTrace, want ...observed) {
  t.Helper()
  got := trace.snapshot()
  if len(got) != len(want) { t.Fatalf("semantic observations=%%#v want=%%#v", got, want) }
  for position := range want {
    if got[position] != want[position] { t.Fatalf("semantic observation[%%d]=%%#v want=%%#v", position, got[position], want[position]) }
  }
  trace.reset()
}

func success(operation observe.Operation, statements int, aggregate int64) observed {
  return observed{operation: operation, outcome: observe.OutcomeSuccess, reason: observe.ReasonNone, statements: statements, aggregate: aggregate}
}

func TestGeneratedSemanticSearchIsAuthorizedAndIncremental(t *testing.T) {
  ctx := context.Background()
  database, err := providersqlite.Open(ctx, providersqlite.Config{DataSourceName: %q})
  if err != nil { t.Fatal(err) }
  t.Cleanup(func() { _ = database.Close() })
  specification, _ := embedding.NewSpecification("test", "deterministic", "v1", 3, 8)
  provider := &embedder{specification: specification}
  embeddings, err := embedding.NewRegistry(map[string]embedding.Provider{"content": provider})
  if err != nil { t.Fatal(err) }
  observations := &observationTrace{}
  application, err := app.Open(ctx, app.Config[string]{
    Database: database,
    Embeddings: embeddings,
    Observer: observations,
    Queue: &golemruntime.QueueConfig{Registry: queue.NewRegistry()},
    ResolvePrincipal: func(_ context.Context, principal string) (actor.Actor, error) { return actor.Actor{Private: principal == "private", Denied: principal == "denied"}, nil },
  })
  if err != nil { t.Fatal(err) }
  denied, err := application.ForPrincipal(ctx, "denied")
  if err != nil { t.Fatal(err) }
  deniedRows, err := denied.Posts.SearchRelated(ctx, "alpha", 10)
  var deniedFailure *golem.Error
  if len(deniedRows) != 0 || !errors.As(err, &deniedFailure) || deniedFailure.Code != golem.CodeForbidden || provider.count() != 0 {
    t.Fatalf("denied search rows=%%d error=%%v provider calls=%%d", len(deniedRows), err, provider.count())
  }
  assertSemanticTrace(t, observations)

  // Opening the application enqueues one reconcile per index. Nothing embeds
  // until a worker runs it, and nothing on the read path will do it instead.
  worker, stopWorker := context.WithCancel(ctx)
  workerDone := make(chan error, 1)
  go func() { workerDone <- application.RunQueueWorker(worker) }()
  deadline := time.Now().Add(60 * time.Second)
  for len(observations.snapshot()) < 2 && time.Now().Before(deadline) { time.Sleep(5 * time.Millisecond) }
  stopWorker()
  <-workerDone
  if provider.count() != 3 {
    t.Fatalf("startup reconcile job embedded=%%d want=3", provider.count())
  }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 3),
    success(observe.OperationSemanticRefresh, 11, 3),
  )

  caller, err := application.ForPrincipal(ctx, "public")
  if err != nil { t.Fatal(err) }
  empty, err := caller.Posts.SearchRelated(ctx, "alpha", 10, models.Posts.Title.StartsWith("absent"))
  if err != nil { t.Fatal(err) }
  if len(empty) != 0 || provider.count() != 4 {
    t.Fatalf("empty authorized search rows=%%d provider calls=%%d", len(empty), provider.count())
  }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 1),
    success(observe.OperationSemanticRank, 1, 0),
  )
  ranked, err := caller.Posts.SearchRelated(ctx, "alpha", 10)
  if err != nil { t.Fatal(err) }
  if len(ranked) != 2 || ranked[0].Distance() != 0 || ranked[0].Similarity() != 1 {
    t.Fatalf("public ranks=%%#v", ranked)
  }
  first, _ := golem.Value(ranked[0].Row(), models.Posts.Title).Get()
  second, _ := golem.Value(ranked[1].Row(), models.Posts.Title).Get()
  if first != "public alpha" || second != "public beta" || strings.Contains(first+second, "private") {
    t.Fatalf("authorized titles=%%q,%%q", first, second)
  }
  if provider.count() != 5 { t.Fatalf("initial provider calls=%%d want=5", provider.count()) }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 1),
    success(observe.OperationSemanticRank, 1, 2),
  )
  sourceID, err := golem.ParseUUID("10000000-0000-0000-0000-000000000001")
  if err != nil { t.Fatal(err) }
  beforeSimilar := provider.count()
  similar, err := caller.Posts.SimilarRelated(ctx, models.Posts.ByID.Value(sourceID), 10)
  if err != nil { t.Fatal(err) }
  if provider.count() != beforeSimilar {
    t.Fatalf("similarity embedded a query: calls=%%d want=%%d", provider.count(), beforeSimilar)
  }
  if len(similar) != 1 { t.Fatalf("similar rows=%%#v", similar) }
  for _, item := range similar {
    title, _ := golem.Value(item.Row(), models.Posts.Title).Get()
    if title == "public alpha" { t.Fatalf("source row returned in its own similarity results") }
    if strings.Contains(title, "private") { t.Fatalf("similarity disclosed an unauthorized row: %%q", title) }
  }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticRank, 2, 1),
  )
  hiddenID, err := golem.ParseUUID("10000000-0000-0000-0000-000000000002")
  if err != nil { t.Fatal(err) }
  missingID, err := golem.ParseUUID("10000000-0000-0000-0000-0000000000ff")
  if err != nil { t.Fatal(err) }
  var hiddenFailure *golem.Error
  if _, err := caller.Posts.SimilarRelated(ctx, models.Posts.ByID.Value(hiddenID), 10); !errors.As(err, &hiddenFailure) || hiddenFailure.Code != golem.CodeNotFound {
    t.Fatalf("similarity for an unreadable source error=%%v", err)
  }
  var missingFailure *golem.Error
  if _, err := caller.Posts.SimilarRelated(ctx, models.Posts.ByID.Value(missingID), 10); !errors.As(err, &missingFailure) || missingFailure.Code != golem.CodeNotFound {
    t.Fatalf("similarity for an absent source error=%%v", err)
  }
  if hiddenFailure.Error() != missingFailure.Error() {
    t.Fatalf("unreadable source is distinguishable from absent: %%q vs %%q", hiddenFailure.Error(), missingFailure.Error())
  }
  if provider.count() != beforeSimilar {
    t.Fatalf("refused similarity embedded a query: calls=%%d", provider.count())
  }
  assertSemanticTrace(t, observations)

  trusted, err := application.System().Posts.SearchRelated(ctx, "alpha", 10)
  if err != nil { t.Fatal(err) }
  if len(trusted) != 3 || provider.count() != 6 {
    t.Fatalf("trusted ranks=%%d provider calls=%%d", len(trusted), provider.count())
  }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 1),
    success(observe.OperationSemanticRank, 1, 3),
  )
  if _, err := caller.Posts.SearchRelated(ctx, "alpha", 1); err != nil { t.Fatal(err) }
  if provider.count() != 7 { t.Fatalf("unchanged rows were re-embedded: calls=%%d", provider.count()) }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 1),
    success(observe.OperationSemanticRank, 1, 1),
  )
  // A write through the raw handle is a write Golem never saw: it marks
  // nothing, so no drain carries it and search keeps serving the last good
  // vector. Reconciliation is the only thing that observes it.
  if _, err := database.UnsafeSQLX().Exec("UPDATE \"posts\" SET \"body\"='alpha revised' WHERE \"title\"='public beta'"); err != nil { t.Fatal(err) }
  if _, err := caller.Posts.SearchRelated(ctx, "alpha", 1); err != nil { t.Fatal(err) }
  if provider.count() != 8 { t.Fatalf("search embedded a source document: calls=%%d want=8", provider.count()) }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 1),
    success(observe.OperationSemanticRank, 1, 1),
  )
  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  if provider.count() != 9 { t.Fatalf("reconcile calls=%%d want=9", provider.count()) }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 1),
    success(observe.OperationSemanticRefresh, 5, 1),
  )
  if _, err := database.UnsafeSQLX().Exec("DELETE FROM \"posts\" WHERE \"title\"='public beta'"); err != nil { t.Fatal(err) }
  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  if provider.count() != 9 { t.Fatalf("delete cleanup unexpectedly embedded: calls=%%d", provider.count()) }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticRefresh, 4, 1),
  )
  afterDelete, err := caller.Posts.SearchRelated(ctx, "alpha", 10)
  if err != nil { t.Fatal(err) }
  if len(afterDelete) != 1 || provider.count() != 10 {
    t.Fatalf("delete cleanup rows=%%d provider calls=%%d", len(afterDelete), provider.count())
  }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 1),
    success(observe.OperationSemanticRank, 1, 1),
  )

  server, err := application.GraphQL(app.GraphQLConfig[string]{
    PrincipalFromContext: func(context.Context) (string, bool) { return "public", true },
	ReportInternalError: func(context.Context, error) {},
  })
  if err != nil { t.Fatal(err) }
  request := httptest.NewRequest("POST", "/graphql", bytes.NewBufferString("{\"query\":\"query { searchPostsByRelated(query: \\\"alpha\\\", take: 10) { title } }\"}"))
  request.Header.Set("Content-Type", "application/json")
  response := httptest.NewRecorder()
  server.Handler().ServeHTTP(response, request)
  if response.Code != 200 { t.Fatalf("semantic GraphQL status=%%d body=%%s", response.Code, response.Body.String()) }
  var envelope struct {
    Data struct { Search []struct { Title string `+"`json:\"title\"`"+` } `+"`json:\"searchPostsByRelated\"`"+` } `+"`json:\"data\"`"+`
    Errors []json.RawMessage `+"`json:\"errors\"`"+`
  }
  if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil { t.Fatal(err) }
  if len(envelope.Errors) != 0 || len(envelope.Data.Search) != 1 || envelope.Data.Search[0].Title != "public alpha" {
    t.Fatalf("semantic GraphQL response=%%s", response.Body.String())
  }

  oversized := httptest.NewRequest("POST", "/graphql", bytes.NewBufferString("{\"query\":\"query { searchPostsByRelated(query: \\\"alpha\\\", take: 501) { title } }\"}"))
  oversized.Header.Set("Content-Type", "application/json")
  oversizedResponse := httptest.NewRecorder()
  server.Handler().ServeHTTP(oversizedResponse, oversized)
  if !strings.Contains(oversizedResponse.Body.String(), "\"errors\":[{") {
    t.Fatalf("oversized semantic GraphQL response=%%s", oversizedResponse.Body.String())
  }

	limited, err := application.GraphQL(app.GraphQLConfig[string]{
		PrincipalFromContext: func(context.Context) (string, bool) { return "public", true },
		Limits: golemgraphql.Limits{MaxPageSize: 1},
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil { t.Fatal(err) }
	overRuntimeLimit := httptest.NewRequest("POST", "/graphql", bytes.NewBufferString("{\"query\":\"query { searchPostsByRelated(query: \\\"alpha\\\", take: 2) { title } }\"}"))
	overRuntimeLimit.Header.Set("Content-Type", "application/json")
	overRuntimeLimitResponse := httptest.NewRecorder()
	limited.Handler().ServeHTTP(overRuntimeLimitResponse, overRuntimeLimit)
	if !strings.Contains(overRuntimeLimitResponse.Body.String(), "\"errors\":[{") {
		t.Fatalf("runtime-limited semantic GraphQL response=%%s", overRuntimeLimitResponse.Body.String())
	}
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 1),
    success(observe.OperationSemanticRank, 1, 1),
  )

  // The similarity root is compiled and bound by different code than search:
  // its selector must decode into the same authorized unique read. Give the
  // source a neighbour so an empty page cannot pass for a working resolver.
  if _, err := database.UnsafeSQLX().Exec("INSERT INTO \"posts\" (\"id\",\"title\",\"body\") VALUES ('10000000-0000-0000-0000-000000000004','public alpha twin','shared topic')"); err != nil { t.Fatal(err) }
  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticProvider, 0, 1),
    success(observe.OperationSemanticRefresh, 5, 1),
  )
  beforeSimilarGraphQL := provider.count()
  similarRequest := httptest.NewRequest("POST", "/graphql", bytes.NewBufferString("{\"query\":\"query { similarPostsByRelated(source: {ID: \\\"10000000-0000-0000-0000-000000000001\\\"}, take: 10) { title } }\"}"))
  similarRequest.Header.Set("Content-Type", "application/json")
  similarResponse := httptest.NewRecorder()
  server.Handler().ServeHTTP(similarResponse, similarRequest)
  if similarResponse.Code != 200 { t.Fatalf("similarity GraphQL status=%%d body=%%s", similarResponse.Code, similarResponse.Body.String()) }
  var similarEnvelope struct {
    Data struct { Similar []struct { Title string `+"`json:\"title\"`"+` } `+"`json:\"similarPostsByRelated\"`"+` } `+"`json:\"data\"`"+`
    Errors []json.RawMessage `+"`json:\"errors\"`"+`
  }
  if err := json.Unmarshal(similarResponse.Body.Bytes(), &similarEnvelope); err != nil { t.Fatal(err) }
  if len(similarEnvelope.Errors) != 0 || len(similarEnvelope.Data.Similar) != 1 || similarEnvelope.Data.Similar[0].Title != "public alpha twin" {
    t.Fatalf("similarity GraphQL response=%%s", similarResponse.Body.String())
  }
  for _, item := range similarEnvelope.Data.Similar {
    if item.Title == "public alpha" { t.Fatal("similarity GraphQL returned its own source row") }
    if strings.Contains(item.Title, "private") { t.Fatalf("similarity GraphQL disclosed an unauthorized row: %%q", item.Title) }
  }
  if provider.count() != beforeSimilarGraphQL {
    t.Fatalf("similarity GraphQL embedded a query: calls=%%d want=%%d", provider.count(), beforeSimilarGraphQL)
  }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticRank, 2, 1),
  )

  defaulted := httptest.NewRequest("POST", "/graphql", bytes.NewBufferString("{\"query\":\"query { similarPostsByRelated(source: {ID: \\\"10000000-0000-0000-0000-000000000001\\\"}) { title } }\"}"))
  defaulted.Header.Set("Content-Type", "application/json")
  defaultedResponse := httptest.NewRecorder()
  server.Handler().ServeHTTP(defaultedResponse, defaulted)
  var defaultedEnvelope struct {
    Data struct { Similar []struct { Title string `+"`json:\"title\"`"+` } `+"`json:\"similarPostsByRelated\"`"+` } `+"`json:\"data\"`"+`
    Errors []json.RawMessage `+"`json:\"errors\"`"+`
  }
  if err := json.Unmarshal(defaultedResponse.Body.Bytes(), &defaultedEnvelope); err != nil { t.Fatal(err) }
  if defaultedResponse.Code != 200 || len(defaultedEnvelope.Errors) != 0 || len(defaultedEnvelope.Data.Similar) != 1 || defaultedEnvelope.Data.Similar[0].Title != "public alpha twin" {
    t.Fatalf("defaulted-take similarity GraphQL response=%%s", defaultedResponse.Body.String())
  }
  if provider.count() != beforeSimilarGraphQL {
    t.Fatalf("defaulted-take similarity GraphQL embedded a query: calls=%%d", provider.count())
  }
  assertSemanticTrace(t, observations,
    success(observe.OperationSemanticRank, 2, 1),
  )
}
`, "file:"+databasePath))
	command := exec.Command("go", "test", "-mod=mod", "-count=1", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh semantic consumer failed: %v\n%s", err, output)
	}
}
