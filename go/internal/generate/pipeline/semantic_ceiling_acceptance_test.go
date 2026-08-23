package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/physical"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// TestFreshGeneratedSemanticSQLiteRanksTheWholeAuthorizedCorpus is the ceiling
// gate. A public catalogue larger than the retired portable candidate ceiling
// must rank end to end, the globally nearest row must stay hidden when policy
// hides it, a page wider than one identity chunk must come back whole, and
// every primary-key shape must map its ranked identity back to a row.
func TestFreshGeneratedSemanticSQLiteRanksTheWholeAuthorizedCorpus(t *testing.T) {
	root := t.TempDir()
	writePipelineAcceptanceFile(t, root, "go.mod", fmt.Sprintf(`module example.com/ceilingapp

go 1.25

require github.com/eleven-am/golem/go v0.0.0

replace github.com/eleven-am/golem/go => %s
`, moduleRoot(t)))
	writePipelineAcceptanceFile(t, root, "actor/actor.go", "package actor\ntype Actor struct { Trusted bool }\n")
	modelSource := strings.ReplaceAll(`package models

import (
  "example.com/ceilingapp/actor"
  "github.com/eleven-am/golem/go/golem"
)

type Doc struct {
  _ struct{} §golem:"model;id=ceiling.Doc;table=docs"§
  ID string §db:"id" golem:"id=ceiling.Doc.ID;pk"§
  Body string §db:"body"§
  Hidden bool §db:"hidden"§
}

func (Doc) GolemModel() golem.ModelSpec[Doc] {
  return golem.DefineModel(golem.SemanticIndex("related", "content", Docs.Body))
}

func (Doc) DefinePolicy(rules *golem.Rules[Doc], value actor.Actor) {
  if value.Trusted {
    rules.CanRead(golem.All[Doc]())
    return
  }
  rules.CanRead(Docs.Hidden.Eq(false))
}

type Item struct {
  _ struct{} §golem:"model;id=ceiling.Item;table=items"§
  ID int64 §db:"id" golem:"id=ceiling.Item.ID;pk"§
  Body string §db:"body"§
}

func (Item) GolemModel() golem.ModelSpec[Item] {
  return golem.DefineModel(golem.SemanticIndex("related", "content", Items.Body))
}

func (Item) DefinePolicy(rules *golem.Rules[Item], _ actor.Actor) {
  rules.CanRead(golem.All[Item]())
}

type Pair struct {
  _ struct{} §golem:"model;id=ceiling.Pair;table=pairs"§
  _ struct{} §golem:"primary=pk_pairs(tenant,serial)"§
  Tenant string §db:"tenant"§
  Serial int64 §db:"serial"§
  Body string §db:"body"§
}

func (Pair) GolemModel() golem.ModelSpec[Pair] {
  return golem.DefineModel(golem.SemanticIndex("related", "content", Pairs.Body))
}

func (Pair) DefinePolicy(rules *golem.Rules[Pair], _ actor.Actor) {
  rules.CanRead(golem.All[Pair]())
}
`, "§", "`")
	writePipelineAcceptanceFile(t, root, "models/models.go", modelSource)
	writePipelineAcceptanceFile(t, root, "schema/schema.go", `package schema

import (
  "example.com/ceilingapp/actor"
  "example.com/ceilingapp/models"
  "github.com/eleven-am/golem/go/golem"
)

func DefineSchema(schema *golem.Schema) {
  golem.SchemaName(schema, "semantic_ceiling")
  golem.Actor[actor.Actor](schema)
  golem.Model[models.Doc](schema)
  golem.Model[models.Item](schema)
  golem.Model[models.Pair](schema)
  golem.Providers(schema, golem.SQLite)
  golem.EmbeddingSpace(schema, "content", 3)
}
`)
	writePipelineAcceptanceFile(t, root, "app/doc.go", "package app\n")
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	tidy.Env = append(os.Environ(), "GOWORK=off")
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("prepare semantic ceiling consumer: %v\n%s", err, output)
	}

	request := Request{
		Compile:    compile.Config{Dir: root, Pattern: "./schema", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.com/ceilingapp/app", PackageName: "app", Directory: filepath.Join(root, "app")},
		Lowerers:   []physical.Lowerer{sqliteprovider.New()},
		Env:        []string{"GOWORK=off"},
	}
	reviewed := p8BuildWithReviewedSQLiteHistory(t, context.Background(), request)
	writeP5ScalarGeneratedArtifacts(t, root, reviewed.Result.Prospective.Artifacts)
	databasePath := filepath.Join(t.TempDir(), "semantic-ceiling.db")
	database, _, err := sqliteprovider.New().Open(context.Background(), "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	p8ApplyReviewedSQLiteHistory(t, context.Background(), database, reviewed.History)
	// Distance decreases with the ordinal, so the nearest rows are the last ones
	// by record key: any statement that truncates the authorized candidate set
	// loses exactly the rows the ranked page must contain.
	if _, err := database.Exec(`WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM seq WHERE i<10001)
INSERT INTO "docs" ("id","body","hidden") SELECT printf('k%06d',i), 'w='||((10001-i)/20.0), i=9995 FROM seq`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM seq WHERE i<4)
INSERT INTO "items" ("id","body") SELECT i*7, 'w='||((4-i)/20.0) FROM seq`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM seq WHERE i<4)
INSERT INTO "pairs" ("tenant","serial","body") SELECT printf('t%d',i%2), i*3, 'w='||((4-i)/20.0) FROM seq`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	writePipelineAcceptanceFile(t, root, "acceptance/ceiling_test.go", strings.ReplaceAll(`package acceptance_test

import (
  "context"
  "fmt"
  "strconv"
  "strings"
  "sync"
  "testing"

  "example.com/ceilingapp/actor"
  "example.com/ceilingapp/app"
  "example.com/ceilingapp/models"
  "github.com/eleven-am/golem/go/embedding"
  "github.com/eleven-am/golem/go/golem"
  providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

type embedder struct {
  specification embedding.Specification
  mu sync.Mutex
}

func (value *embedder) Specification() embedding.Specification { return value.specification }

// Embed reads the weight authored into each source document so distances are
// exact and independent of any provider heuristics.
func (value *embedder) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
  value.mu.Lock()
  defer value.mu.Unlock()
  result := make([]embedding.Vector, len(inputs))
  for index, input := range inputs {
    weight := 0.0
    if position := strings.LastIndex(input.Text(), "w="); position >= 0 {
      parsed, err := strconv.ParseFloat(strings.TrimRight(input.Text()[position+2:], "\x00"), 64)
      if err != nil { return nil, err }
      weight = parsed
    }
    vector, err := embedding.NewVector([]float32{1, float32(weight), 0})
    if err != nil { return nil, err }
    result[index] = vector
  }
  return result, nil
}

func openApplication(t *testing.T) *app.App[string] {
  t.Helper()
  ctx := context.Background()
  database, err := providersqlite.Open(ctx, providersqlite.Config{DataSourceName: {{DSN}}})
  if err != nil { t.Fatal(err) }
  t.Cleanup(func() { _ = database.Close() })
  specification, err := embedding.NewSpecification("test", "deterministic", "v1", 3, 256)
  if err != nil { t.Fatal(err) }
  embeddings, err := embedding.NewRegistry(map[string]embedding.Provider{"content": &embedder{specification: specification}})
  if err != nil { t.Fatal(err) }
  application, err := app.Open(ctx, app.Config[string]{
    Database: database,
    Embeddings: embeddings,
    ResolvePrincipal: func(_ context.Context, principal string) (actor.Actor, error) { return actor.Actor{Trusted: principal == "trusted"}, nil },
  })
  if err != nil { t.Fatal(err) }
  return application
}

// authorizedKeys returns the exact ranked page: ordinals descend from the
// nearest row and skip the one policy hides.
func authorizedKeys(count int) []string {
  result := make([]string, 0, count)
  for ordinal := 10001; len(result) < count; ordinal-- {
    if ordinal == 9995 { continue }
    result = append(result, fmt.Sprintf("k%06d", ordinal))
  }
  return result
}

func docKeys(t *testing.T, results []golem.SemanticResult[models.Doc]) []string {
  t.Helper()
  keys := make([]string, len(results))
  for index, item := range results {
    value, ok := golem.Value(item.Row(), models.Docs.ID).Get()
    if !ok { t.Fatalf("ranked row %d has no readable identity", index) }
    keys[index] = value
  }
  return keys
}

func TestGeneratedSemanticSearchHasNoAuthorizedCandidateCeiling(t *testing.T) {
  ctx := context.Background()
  caller, err := openApplication(t).ForPrincipal(ctx, "public")
  if err != nil { t.Fatal(err) }
  ranked, err := caller.Docs.SearchRelated(ctx, "w=0", 20)
  if err != nil { t.Fatal(err) }
  got, want := docKeys(t, ranked), authorizedKeys(20)
  if strings.Join(got, ",") != strings.Join(want, ",") {
    t.Fatalf("authorized page=%v want=%v", got, want)
  }
  for _, key := range got {
    if key == "k009995" { t.Fatal("policy-hidden row occupied a ranked slot") }
  }
}

// TestGeneratedSemanticSearchReadsEveryIdentityChunk pins that a ranked page
// wider than one bounded row statement is assembled whole and in rank order.
func TestGeneratedSemanticSearchReadsEveryIdentityChunk(t *testing.T) {
  ctx := context.Background()
  caller, err := openApplication(t).ForPrincipal(ctx, "public")
  if err != nil { t.Fatal(err) }
  ranked, err := caller.Docs.SearchRelated(ctx, "w=0", 250)
  if err != nil { t.Fatal(err) }
  got, want := docKeys(t, ranked), authorizedKeys(250)
  if len(got) != 250 { t.Fatalf("chunked page=%d want=250", len(got)) }
  if strings.Join(got, ",") != strings.Join(want, ",") {
    t.Fatalf("chunked page=%v want=%v", got[:5], want[:5])
  }
}

// TestGeneratedSemanticSimilarityExcludesItsOwnSource keeps the source out of
// its neighbourhood while the rest of the authorized corpus still ranks.
func TestGeneratedSemanticSimilarityExcludesItsOwnSource(t *testing.T) {
  ctx := context.Background()
  caller, err := openApplication(t).ForPrincipal(ctx, "public")
  if err != nil { t.Fatal(err) }
  similar, err := caller.Docs.SimilarRelated(ctx, models.Docs.ByID.Value("k010001"), 20)
  if err != nil { t.Fatal(err) }
  got := docKeys(t, similar)
  if len(got) != 20 { t.Fatalf("similar page=%d want=20", len(got)) }
  if strings.Join(got, ",") != strings.Join(authorizedKeys(21)[1:], ",") {
    t.Fatalf("similar page=%v", got)
  }
  for _, key := range got {
    if key == "k010001" { t.Fatal("similarity source returned in its own results") }
    if key == "k009995" { t.Fatal("policy-hidden row occupied a similarity slot") }
  }
}

// TestGeneratedSemanticRanksMapBackAcrossPrimaryKeyShapes covers an integer
// key and a composite key: every ranked identity must resolve to its row, in
// the exact stored component order.
func TestGeneratedSemanticRanksMapBackAcrossPrimaryKeyShapes(t *testing.T) {
  ctx := context.Background()
  caller, err := openApplication(t).ForPrincipal(ctx, "public")
  if err != nil { t.Fatal(err) }
  items, err := caller.Items.SearchRelated(ctx, "w=0", 5)
  if err != nil { t.Fatal(err) }
  if len(items) != 5 { t.Fatalf("integer-key ranks=%d want=5", len(items)) }
  wantItems := []int64{28, 21, 14, 7, 0}
  for index, item := range items {
    value, ok := golem.Value(item.Row(), models.Items.ID).Get()
    if !ok || value != wantItems[index] { t.Fatalf("integer-key rank[%d]=%v ok=%t want=%d", index, value, ok, wantItems[index]) }
  }
  pairs, err := caller.Pairs.SearchRelated(ctx, "w=0", 5)
  if err != nil { t.Fatal(err) }
  if len(pairs) != 5 { t.Fatalf("composite-key ranks=%d want=5", len(pairs)) }
  wantTenants, wantSerials := []string{"t0", "t1", "t0", "t1", "t0"}, []int64{12, 9, 6, 3, 0}
  for index, pair := range pairs {
    tenant, tenantOK := golem.Value(pair.Row(), models.Pairs.Tenant).Get()
    serial, serialOK := golem.Value(pair.Row(), models.Pairs.Serial).Get()
    if !tenantOK || !serialOK || tenant != wantTenants[index] || serial != wantSerials[index] {
      t.Fatalf("composite-key rank[%d]=%q/%v want=%q/%d", index, tenant, serial, wantTenants[index], wantSerials[index])
    }
  }
}
`, "{{DSN}}", strconv.Quote("file:"+databasePath)))
	command := exec.Command("go", "test", "-mod=mod", "-count=1", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("semantic ceiling consumer failed: %v\n%s", err, output)
	}
}
