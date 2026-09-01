package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresqlprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

// TestFreshGeneratedSemanticPostgreSQLApplicationOwnsPGVectorLifecycle is an
// opt-in live test. The DSN must name a disposable PostgreSQL database with
// permission to install pgvector; completion evidence runs it against the
// official pgvector/pgvector:pg17 image.
func TestFreshGeneratedSemanticPostgreSQLApplicationOwnsPGVectorLifecycle(t *testing.T) {
	dsn := os.Getenv("GOLEM_TEST_PGVECTOR_DSN")
	if dsn == "" {
		if os.Getenv("GOLEM_REQUIRE_PGVECTOR") == "1" {
			t.Fatal("GOLEM_TEST_PGVECTOR_DSN is required")
		}
		t.Skip("GOLEM_TEST_PGVECTOR_DSN is not configured")
	}
	ctx := context.Background()
	const namespace = "semantic_pg_acceptance"
	root := t.TempDir()
	writePipelineAcceptanceFile(t, root, "go.mod", fmt.Sprintf(`module example.com/semanticpgapp

go 1.25

require github.com/eleven-am/golem/go v0.0.0

replace github.com/eleven-am/golem/go => %s
`, moduleRoot(t)))
	writePipelineAcceptanceFile(t, root, "actor/actor.go", "package actor\ntype Actor struct { Private bool }\n")
	modelSource := strings.ReplaceAll(`package models

import (
  "example.com/semanticpgapp/actor"
  "github.com/eleven-am/golem/go/golem"
)

type Post struct {
  _ struct{} §golem:"model;id=semanticpg.Post;table=posts"§
  ID golem.UUID §db:"id" golem:"id=semanticpg.Post.ID;pk"§
  Title string §db:"title"§
  Body string §db:"body"§
  Published bool §db:"published"§
}

func (Post) GolemModel() golem.ModelSpec[Post] {
  return golem.DefineModel(golem.SemanticIndex("related", "content", Posts.Title, Posts.Body))
}

func (Post) DefinePolicy(rules *golem.Rules[Post], value actor.Actor) {
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
  "example.com/semanticpgapp/actor"
  "example.com/semanticpgapp/models"
  "github.com/eleven-am/golem/go/golem"
)

func DefineSchema(schema *golem.Schema) {
  golem.SchemaName(schema, "semantic_pg_acceptance")
  golem.Actor[actor.Actor](schema)
  golem.Model[models.Post](schema)
  golem.Providers(schema, golem.PostgreSQL)
  golem.EmbeddingSpace(schema, "content", 3)
}
`)
	writePipelineAcceptanceFile(t, root, "app/doc.go", "package app\n")
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	tidy.Env = append(os.Environ(), "GOWORK=off")
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("prepare PostgreSQL semantic consumer: %v\n%s", err, output)
	}

	request := Request{
		Compile:      compile.Config{Dir: root, Pattern: "./schema", Root: "DefineSchema"},
		AppPackage:   modelcodegen.PackageSpec{ImportPath: "example.com/semanticpgapp/app", PackageName: "app", Directory: filepath.Join(root, "app")},
		Lowerers:     []physical.Lowerer{postgresqlprovider.New()},
		LowerOptions: []ProviderOptions{{Provider: compilerir.PostgreSQL, Options: physical.LowerOptions{Namespace: namespace}}},
		Env:          []string{"GOWORK=off"},
	}
	initial, err := Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Providers) != 1 || initial.Providers[0].Provider.Provider != compilerir.PostgreSQL {
		t.Fatalf("PostgreSQL semantic providers=%#v", initial.Providers)
	}
	history := initialPostgreSQLSemanticHistory(t, initial.ModelFingerprint, initial.Providers[0].Schema)
	request.ReviewedMigrations = []ReviewedMigration{history}
	bound, err := Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	writeP5ScalarGeneratedArtifacts(t, root, bound.Prospective.Artifacts)

	desired := bound.Providers[0].Schema
	if string(desired.Namespace.Name) != namespace || len(desired.Extensions) != 1 {
		t.Fatalf("PostgreSQL semantic schema namespace=%q extensions=%d", desired.Namespace.Name, len(desired.Extensions))
	}
	descriptor, err := semanticstorage.Decode(desired.Extensions[0])
	if err != nil {
		t.Fatal(err)
	}
	vectorTable := string(descriptor.Storage) + "_vec"
	stateTable := string(descriptor.Storage) + "_state"
	hnswIndex := string(descriptor.Storage) + "_hnsw"

	provider := postgresqlprovider.New()
	database, report, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "` + namespace + `" CASCADE`)
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	}
	cleanup()
	t.Cleanup(func() {
		cleanup()
		_ = database.Close()
	})
	if report.Version.Major != 17 {
		t.Fatalf("PostgreSQL major=%d want=17", report.Version.Major)
	}
	if err := provider.ApplyMigration(ctx, database, history.Manifest, history.Files); err != nil {
		t.Fatalf("apply reviewed PostgreSQL semantic history: %v", err)
	}
	if err := provider.Verify(ctx, database, desired); err != nil {
		t.Fatalf("introspect reviewed PostgreSQL semantic schema: %v", err)
	}
	var extensionVersion, method, opclass string
	var valid, ready bool
	if err := database.Get(&extensionVersion, `SELECT extversion FROM pg_catalog.pg_extension WHERE extname='vector'`); err != nil || extensionVersion == "" {
		t.Fatalf("pgvector extension version=%q error=%v", extensionVersion, err)
	}
	if err := database.QueryRowx(`SELECT am.amname,opc.opcname,i.indisvalid,i.indisready
FROM pg_catalog.pg_index i
JOIN pg_catalog.pg_class ci ON ci.oid=i.indexrelid
JOIN pg_catalog.pg_class ct ON ct.oid=i.indrelid
JOIN pg_catalog.pg_namespace n ON n.oid=ct.relnamespace
JOIN pg_catalog.pg_am am ON am.oid=ci.relam
JOIN pg_catalog.pg_opclass opc ON opc.oid=i.indclass[0]
WHERE n.nspname=$1 AND ct.relname=$2 AND ci.relname=$3`, namespace, vectorTable, hnswIndex).Scan(&method, &opclass, &valid, &ready); err != nil {
		t.Fatal(err)
	}
	if method != "hnsw" || opclass != "vector_cosine_ops" || !valid || !ready {
		t.Fatalf("HNSW facts method=%q opclass=%q valid=%t ready=%t", method, opclass, valid, ready)
	}
	acceptance := `package acceptance_test

import (
  "context"
  "strings"
  "sync"
  "testing"
  "time"

  "example.com/semanticpgapp/actor"
  "example.com/semanticpgapp/app"
  "example.com/semanticpgapp/models"
  "github.com/eleven-am/golem/go/embedding"
  "github.com/eleven-am/golem/go/golem"
  "github.com/eleven-am/golem/go/observe"
  providerpostgresql "github.com/eleven-am/golem/go/provider/postgresql"
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
  statements int
  aggregate int64
}

type observationTrace struct {
  mu sync.Mutex
  values []observed
}

func (trace *observationTrace) ObserveGolem(_ context.Context, value observe.Observation) {
  if value.Kind() != observe.KindSemantic { return }
  if value.Outcome() != observe.OutcomeSuccess || value.Reason() != observe.ReasonNone { panic("unexpected semantic observation outcome") }
  trace.mu.Lock()
  defer trace.mu.Unlock()
  trace.values = append(trace.values, observed{operation: value.Operation(), statements: value.StatementCount(), aggregate: value.AggregateCount()})
}

func (trace *observationTrace) take() []observed {
  trace.mu.Lock()
  defer trace.mu.Unlock()
  result := append([]observed(nil), trace.values...)
  trace.values = nil
  return result
}

func assertTrace(t *testing.T, trace *observationTrace, want ...observed) {
  t.Helper()
  got := trace.take()
  if len(got) != len(want) { t.Fatalf("semantic observations=%#v want=%#v", got, want) }
  for index := range want {
    if got[index] != want[index] { t.Fatalf("semantic observation[%d]=%#v want=%#v", index, got[index], want[index]) }
  }
}

func uuid(t *testing.T, text string) golem.UUID {
  t.Helper()
  value, err := golem.ParseUUID(text)
  if err != nil { t.Fatal(err) }
  return value
}

func TestGeneratedPGVectorSearchIsNativeAuthorizedAndIncremental(t *testing.T) {
  ctx := context.Background()
  database, err := providerpostgresql.Open(ctx, providerpostgresql.Config{DataSourceName: {{DSN}}})
  if err != nil { t.Fatal(err) }
  t.Cleanup(func() { _ = database.Close() })
  specification, err := embedding.NewSpecification("test", "deterministic", "v1", 3, 8)
  if err != nil { t.Fatal(err) }
  provider := &embedder{specification: specification}
  embeddings, err := embedding.NewRegistry(map[string]embedding.Provider{"content": provider})
  if err != nil { t.Fatal(err) }
  observations := &observationTrace{}
  application, err := app.Open(ctx, app.Config[string]{
    Database: database,
    Embeddings: embeddings,
    Observer: observations,
    Queue: &golemruntime.QueueConfig{Registry: queue.NewRegistry()},
    ResolvePrincipal: func(_ context.Context, principal string) (actor.Actor, error) { return actor.Actor{Private: principal == "private"}, nil },
  })
  if err != nil { t.Fatal(err) }
  ids := []golem.UUID{
    uuid(t, "10000000-0000-0000-0000-000000000001"),
    uuid(t, "10000000-0000-0000-0000-000000000002"),
    uuid(t, "10000000-0000-0000-0000-000000000003"),
    uuid(t, "10000000-0000-0000-0000-000000000004"),
  }
  rows := []struct{ title, body string; published bool }{
    {"public alpha", "shared topic", true},
    {"private alpha", "secret protected canary", true},
    {"public beta", "other topic", true},
    {"public alpha twin", "shared topic", true},
  }
  for index, row := range rows {
    if _, err := application.System().Posts.Create(ctx, models.Posts.Create(
      models.Posts.ID.Create(ids[index]), models.Posts.Title.Create(row.title),
      models.Posts.Body.Create(row.body), models.Posts.Published.Create(row.published),
    )); err != nil { t.Fatal(err) }
  }
  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  if provider.count() != 4 { t.Fatalf("initial refresh calls=%d want=4", provider.count()) }
  assertTrace(t, observations,
    observed{operation: observe.OperationSemanticProvider, aggregate: 4},
    observed{operation: observe.OperationSemanticRefresh, statements: 6, aggregate: 4},
  )

  var vectorCount, stateCount int
  if err := database.UnsafeSQLX().Get(&vectorCount, "SELECT count(*) FROM \"{{NS}}\".\"{{VECTOR}}\""); err != nil { t.Fatal(err) }
  if err := database.UnsafeSQLX().Get(&stateCount, "SELECT count(*) FROM \"{{NS}}\".\"{{STATE}}\" WHERE status='ready'"); err != nil { t.Fatal(err) }
  if vectorCount != 4 || stateCount != 4 { t.Fatalf("managed semantic rows vectors=%d ready=%d", vectorCount, stateCount) }

  caller, err := application.ForPrincipal(ctx, "public")
  if err != nil { t.Fatal(err) }
  ranked, err := caller.Posts.SearchRelated(ctx, "alpha", 10)
  if err != nil { t.Fatal(err) }
  if provider.count() != 5 { t.Fatalf("query provider calls=%d want=5", provider.count()) }
  if len(ranked) != 3 || ranked[0].Distance() != 0 || ranked[0].Similarity() != 1 || ranked[1].Distance() != 0 {
    t.Fatalf("public ranks=%#v", ranked)
  }
  first, _ := golem.Value(ranked[0].Row(), models.Posts.Title).Get()
  second, _ := golem.Value(ranked[1].Row(), models.Posts.Title).Get()
  third, _ := golem.Value(ranked[2].Row(), models.Posts.Title).Get()
  if first != "public alpha" || second != "public alpha twin" || third != "public beta" || strings.Contains(first+second+third, "private") {
    t.Fatalf("authorized titles=%q,%q", first, second)
  }
  assertTrace(t, observations,
    observed{operation: observe.OperationSemanticProvider, aggregate: 1},
    observed{operation: observe.OperationSemanticRank, statements: 2, aggregate: 3},
  )

  similarSourceID, err := golem.ParseUUID("10000000-0000-0000-0000-000000000001")
  if err != nil { t.Fatal(err) }
  beforeSimilar := provider.count()
  similar, err := caller.Posts.SimilarRelated(ctx, models.Posts.ByID.Value(similarSourceID), 10)
  if err != nil { t.Fatal(err) }
  if provider.count() != beforeSimilar {
    t.Fatalf("pgvector similarity embedded a query: calls=%d want=%d", provider.count(), beforeSimilar)
  }
  if len(similar) != 2 { t.Fatalf("pgvector similar rows=%#v", similar) }
  similarFirst, _ := golem.Value(similar[0].Row(), models.Posts.Title).Get()
  similarSecond, _ := golem.Value(similar[1].Row(), models.Posts.Title).Get()
  if similarFirst != "public alpha twin" || similarSecond != "public beta" {
    t.Fatalf("pgvector similar titles=%q,%q", similarFirst, similarSecond)
  }
  if similar[0].Distance() != 0 {
    t.Fatalf("pgvector stored-vector distance=%v want=0", similar[0].Distance())
  }
  if strings.Contains(similarFirst+similarSecond, "private") || similarFirst == "public alpha" || similarSecond == "public alpha" {
    t.Fatalf("pgvector similarity leaked source or private row: %q,%q", similarFirst, similarSecond)
  }
  assertTrace(t, observations,
    observed{operation: observe.OperationSemanticRank, statements: 3, aggregate: 2},
  )

  tx, err := database.UnsafeSQLX().BeginTxx(ctx, nil)
  if err != nil { t.Fatal(err) }
  defer tx.Rollback()
  if _, err := tx.ExecContext(ctx, "SET LOCAL enable_seqscan=off"); err != nil { t.Fatal(err) }
  candidateKeys := make([]string, 0, 3)
  if err := tx.SelectContext(ctx, &candidateKeys, "SELECT record_key FROM \"{{NS}}\".\"{{VECTOR}}\" ORDER BY record_key"); err != nil { t.Fatal(err) }
  nativeRows, err := tx.QueryxContext(ctx, "SELECT (embedding <=> $1::vector)::double precision AS distance FROM \"{{NS}}\".\"{{VECTOR}}\" WHERE record_key=ANY($2::text[]) ORDER BY embedding <=> $1::vector LIMIT $3", "[1,0,0]", candidateKeys, 4)
  if err != nil { t.Fatal(err) }
  distances := make([]float64, 0, 4)
  for nativeRows.Next() { var distance float64; if err := nativeRows.Scan(&distance); err != nil { t.Fatal(err) }; distances = append(distances, distance) }
  if err := nativeRows.Close(); err != nil { t.Fatal(err) }
  if len(distances) != 4 || distances[0] != 0 || distances[1] != 0 || distances[2] != 0 || distances[3] <= distances[2] {
    t.Fatalf("native cosine ordering=%v", distances)
  }
  if err := tx.Rollback(); err != nil { t.Fatal(err) }

  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  if provider.count() != 5 { t.Fatalf("unchanged refresh re-embedded rows: calls=%d", provider.count()) }
  assertTrace(t, observations, observed{operation: observe.OperationSemanticRefresh, statements: 4})
  if _, err := application.System().Posts.Update(ctx, models.Posts.ByID.Value(ids[2]), models.Posts.Update(models.Posts.Body.Set("alpha revised"))); err != nil { t.Fatal(err) }
  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  if provider.count() != 6 { t.Fatalf("indexed-field refresh calls=%d want=6", provider.count()) }
  assertTrace(t, observations,
    observed{operation: observe.OperationSemanticProvider, aggregate: 1},
    observed{operation: observe.OperationSemanticRefresh, statements: 6, aggregate: 1},
  )
  // Writing a non-indexed field still marks the record: the write path cannot
  // know whether the embedded document changed. Reconciliation settles it with
  // the content hash and flips the record back to ready — one extra statement,
  // one record acted on, and no embedding-provider call.
  if _, err := application.System().Posts.Update(ctx, models.Posts.ByID.Value(ids[2]), models.Posts.Update(models.Posts.Published.Set(false))); err != nil { t.Fatal(err) }
  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  if provider.count() != 6 { t.Fatalf("non-indexed field caused re-embedding: calls=%d", provider.count()) }
  assertTrace(t, observations, observed{operation: observe.OperationSemanticRefresh, statements: 5, aggregate: 1})
  if _, err := application.System().Posts.Delete(ctx, models.Posts.ByID.Value(ids[1])); err != nil { t.Fatal(err) }
  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  assertTrace(t, observations, observed{operation: observe.OperationSemanticRefresh, statements: 6, aggregate: 1})
  if err := database.UnsafeSQLX().Get(&vectorCount, "SELECT count(*) FROM \"{{NS}}\".\"{{VECTOR}}\""); err != nil { t.Fatal(err) }
  if err := database.UnsafeSQLX().Get(&stateCount, "SELECT count(*) FROM \"{{NS}}\".\"{{STATE}}\""); err != nil { t.Fatal(err) }
  if vectorCount != 3 || stateCount != 3 { t.Fatalf("stale semantic cleanup vectors=%d states=%d", vectorCount, stateCount) }

  if _, err := database.UnsafeSQLX().ExecContext(ctx, "INSERT INTO \"{{NS}}\".\"posts\" (id,title,body,published) SELECT md5('planner-'||value::text)::uuid, 'public planner '||lpad(value::text,5,'0'), CASE WHEN value % 3=0 THEN 'alpha planner' ELSE 'beta planner' END, true FROM generate_series(1,1000) value"); err != nil { t.Fatal(err) }
  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  if provider.count() != 1006 { t.Fatalf("planner refresh calls=%d want=1006", provider.count()) }
  bulkTrace := observations.take()
  if len(bulkTrace) != 126 { t.Fatalf("bulk semantic observations=%d", len(bulkTrace)) }
  for index := 0; index < 125; index++ {
    if bulkTrace[index] != (observed{operation: observe.OperationSemanticProvider, aggregate: 8}) { t.Fatalf("bulk provider observation[%d]=%#v", index, bulkTrace[index]) }
  }
  if bulkTrace[125] != (observed{operation: observe.OperationSemanticRefresh, statements: 274, aggregate: 1000}) { t.Fatalf("bulk refresh observation=%#v", bulkTrace[125]) }
  plannerRanks, err := caller.Posts.SearchRelated(ctx, "alpha", 10)
  if err != nil { t.Fatal(err) }
  if len(plannerRanks) != 10 || provider.count() != 1007 { t.Fatalf("generated HNSW ranks=%d calls=%d", len(plannerRanks), provider.count()) }
  for _, rank := range plannerRanks {
    title, _ := golem.Value(rank.Row(), models.Posts.Title).Get()
    if !strings.HasPrefix(title, "public ") || strings.Contains(title, "private") { t.Fatalf("generated HNSW authorization escaped: %q", title) }
  }
  assertTrace(t, observations,
    observed{operation: observe.OperationSemanticProvider, aggregate: 1},
    observed{operation: observe.OperationSemanticRank, statements: 2, aggregate: 10},
  )
  plannerKeys := make([]string, 0, 1003)
  if err := database.UnsafeSQLX().Select(&plannerKeys, "SELECT record_key FROM \"{{NS}}\".\"{{VECTOR}}\" ORDER BY record_key"); err != nil { t.Fatal(err) }
  if len(plannerKeys) != 1003 { t.Fatalf("planner candidate keys=%d", len(plannerKeys)) }
  plannerTx, err := database.UnsafeSQLX().BeginTxx(ctx, nil)
  if err != nil { t.Fatal(err) }
  defer plannerTx.Rollback()
  if _, err := plannerTx.ExecContext(ctx, "SET LOCAL hnsw.iterative_scan='strict_order'"); err != nil { t.Fatal(err) }
  explainRows, err := plannerTx.QueryxContext(ctx, "EXPLAIN (COSTS OFF) SELECT record_key FROM \"{{NS}}\".\"{{VECTOR}}\" WHERE record_key=ANY($2::text[]) ORDER BY embedding <=> $1::vector LIMIT $3", "[1,0,0]", plannerKeys, 42)
  if err != nil { t.Fatal(err) }
  var plan strings.Builder
  for explainRows.Next() { var line string; if err := explainRows.Scan(&line); err != nil { t.Fatal(err) }; plan.WriteString(line); plan.WriteByte('\n') }
  if err := explainRows.Close(); err != nil { t.Fatal(err) }
  if !strings.Contains(plan.String(), "{{HNSW}}") || !strings.Contains(plan.String(), "Index Scan") {
    t.Fatalf("eligible native HNSW plan was not selected:\n%s", plan.String())
  }
  hnswRows, err := plannerTx.QueryxContext(ctx, "SELECT (embedding <=> $1::vector)::double precision FROM \"{{NS}}\".\"{{VECTOR}}\" WHERE record_key=ANY($2::text[]) ORDER BY embedding <=> $1::vector LIMIT $3", "[1,0,0]", plannerKeys, 10)
  if err != nil { t.Fatal(err) }
  hnswCount := 0
  for hnswRows.Next() { var distance float64; if err := hnswRows.Scan(&distance); err != nil { t.Fatal(err) }; if distance != 0 { t.Fatalf("HNSW nearest distance=%v", distance) }; hnswCount++ }
  if err := hnswRows.Close(); err != nil { t.Fatal(err) }
  if hnswCount != 10 { t.Fatalf("HNSW nearest count=%d", hnswCount) }
  if err := plannerTx.Rollback(); err != nil { t.Fatal(err) }
  if _, err := database.UnsafeSQLX().ExecContext(ctx, "DELETE FROM \"{{NS}}\".\"posts\" WHERE title LIKE 'public planner %'"); err != nil { t.Fatal(err) }
  if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
  assertTrace(t, observations, observed{operation: observe.OperationSemanticRefresh, statements: 30, aggregate: 1000})
  if err := database.UnsafeSQLX().Get(&vectorCount, "SELECT count(*) FROM \"{{NS}}\".\"{{VECTOR}}\""); err != nil { t.Fatal(err) }
  if err := database.UnsafeSQLX().Get(&stateCount, "SELECT count(*) FROM \"{{NS}}\".\"{{STATE}}\""); err != nil { t.Fatal(err) }
  if vectorCount != 3 || stateCount != 3 { t.Fatalf("planner cleanup vectors=%d states=%d", vectorCount, stateCount) }

  // The whole loop, with nothing explicit in it: an ordinary write marks its
  // own record and enqueues a drain inside its transaction, a worker runs the
  // drain, and the new document is what search ranks. No RefreshSemanticIndexes
  // anywhere below this line.
  observations.take()
  var queueSchema string
  if err := database.UnsafeSQLX().Get(&queueSchema, "SELECT table_schema FROM information_schema.tables WHERE table_name='golem_queue' LIMIT 1"); err != nil { t.Fatal(err) }
  activeDrains := func() int {
    var count int
    if err := database.UnsafeSQLX().Get(&count, "SELECT count(*) FROM \""+queueSchema+"\".\"golem_queue\" WHERE \"type\"='semantic.drain' AND \"status\" IN ('pending','leased')"); err != nil { t.Fatal(err) }
    return count
  }
  runWorkerUntil := func(done func() bool) {
    workerContext, stop := context.WithCancel(ctx)
    finished := make(chan error, 1)
    go func() { finished <- application.RunQueueWorker(workerContext) }()
    limit := time.Now().Add(60 * time.Second)
    for !done() && time.Now().Before(limit) { time.Sleep(10 * time.Millisecond) }
    stop()
    <-finished
  }
  // Quiesce the drains the earlier writes left behind, so the only drain job
  // that can exist after the next write is the one that write enqueues. Without
  // this the assertion below would pass on a leftover job.
  runWorkerUntil(func() bool { return activeDrains() == 0 })
  if remaining := activeDrains(); remaining != 0 { t.Fatalf("queue did not quiesce: %d drain jobs still active", remaining) }
  beforeDrain := provider.count()
  if _, err := application.System().Posts.Update(ctx, models.Posts.ByID.Value(ids[3]), models.Posts.Update(models.Posts.Body.Set("beta rewritten by the write path"))); err != nil { t.Fatal(err) }
  if enqueued := activeDrains(); enqueued != 1 {
    t.Fatalf("the write enqueued %d drain jobs in its own transaction, want 1", enqueued)
  }
  // Wait for the durable outcome, not for the provider counter: the count rises
  // inside the embed, before the vector and the flip commit, and cancelling
  // there would abort the drain's own transaction.
  drained := -1
  runWorkerUntil(func() bool {
    if err := database.UnsafeSQLX().Get(&drained, "SELECT count(*) FROM \"{{NS}}\".\"{{STATE}}\" WHERE status<>'ready'"); err != nil { t.Fatal(err) }
    return drained == 0 && provider.count() > beforeDrain
  })
  if provider.count() != beforeDrain+1 {
    t.Fatalf("drain job embedded=%d want=1: the write path's mark never reached a worker", provider.count()-beforeDrain)
  }
  if drained != 0 { t.Fatalf("records left marked after the drain: %d", drained) }
  // Before the drain this record's document carried no "beta" and ranked a full
  // unit away from that query. Ranking it at distance zero is the new vector,
  // and nothing but the drain could have stored it.
  rewritten, err := caller.Posts.SearchRelated(ctx, "beta", 10)
  if err != nil { t.Fatal(err) }
  drainedRanked := false
  for _, item := range rewritten {
    title, _ := golem.Value(item.Row(), models.Posts.Title).Get()
    if title != "public alpha twin" { continue }
    if item.Distance() != 0 {
      t.Fatalf("drained record ranks at distance %v against its own new document", item.Distance())
    }
    drainedRanked = true
  }
  if !drainedRanked {
    t.Fatalf("the drained record did not rank against its new document: %d results", len(rewritten))
  }
}
`
	acceptance = strings.NewReplacer(
		"{{DSN}}", strconv.Quote(dsn),
		"{{NS}}", namespace,
		"{{VECTOR}}", vectorTable,
		"{{STATE}}", stateTable,
		"{{HNSW}}", hnswIndex,
	).Replace(acceptance)
	writePipelineAcceptanceFile(t, root, "acceptance/semantic_postgresql_test.go", acceptance)
	command := exec.Command("go", "test", "-mod=mod", "-count=1", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh PostgreSQL semantic consumer failed: %v\n%s", err, output)
	}
}

func initialPostgreSQLSemanticHistory(t *testing.T, modelFingerprint compilerir.Fingerprint, schema physical.PhysicalSchema) ReviewedMigration {
	t.Helper()
	desired, err := physical.Normalize(schema)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := physical.Normalize(physical.PhysicalSchema{
		Version: desired.Version, CanonicalVersion: desired.CanonicalVersion,
		Provider: desired.Provider, Namespace: desired.Namespace,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migration.Diff(empty, desired)
	if err != nil {
		t.Fatal(err)
	}
	emptyModelFingerprint, err := compilerir.ModelFingerprint(compilerir.CanonicalEmptyModel())
	if err != nil {
		t.Fatal(err)
	}
	allowlistFingerprint, err := physical.UnmanagedAllowlistFingerprint(desired)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{
		ID: "0001_initial", Operations: plan.Operations, Phases: plan.Phases,
		BeforeModel: migration.Digest(emptyModelFingerprint), AfterModel: migration.Digest(modelFingerprint),
		BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint,
		BeforeSnapshot: empty, AfterSnapshot: desired,
		UnmanagedAllowlistDigest: migration.Digest(allowlistFingerprint.String()),
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
	}
	script, err := postgresqlprovider.New().RenderInitial(desired)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	base := "migrations/postgresql/0001_initial"
	files := map[string][]byte{
		base + ".sql":                  []byte(script.SQL()),
		base + ".before.snapshot.json": before,
		base + ".after.snapshot.json":  after,
	}
	entry.Files = []migration.FileChecksum{
		{Path: base + ".sql", SHA256: migration.Checksum(files[base+".sql"])},
		{Path: base + ".before.snapshot.json", SHA256: migration.Checksum(before)},
		{Path: base + ".after.snapshot.json", SHA256: migration.Checksum(after)},
	}
	entry.ChainHash = migration.ChainHash(entry)
	manifest := migration.Manifest{
		FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion,
		HashAlgorithm: "sha256", GeneratorVersion: "p9-semantic-postgresql-acceptance", Provider: desired.Provider,
		Entries: []migration.ManifestEntry{entry},
	}
	if _, err := migration.EncodeManifest(manifest, files); err != nil {
		t.Fatal(err)
	}
	return ReviewedMigration{Manifest: manifest, Files: files}
}
