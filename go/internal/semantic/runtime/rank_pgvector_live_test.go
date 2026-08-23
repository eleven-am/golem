package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/jmoiron/sqlx"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const pgvectorRankNamespace = "semantic_rank_live"

type pgvectorRankFixture struct {
	database *sqlx.DB
	manager  *Manager
	index    Index
	storage  string
}

func openPGVectorRankFixture(t *testing.T) pgvectorRankFixture {
	t.Helper()
	dsn := os.Getenv("GOLEM_TEST_PGVECTOR_DSN")
	if dsn == "" {
		if os.Getenv("GOLEM_REQUIRE_PGVECTOR") == "1" {
			t.Fatal("GOLEM_TEST_PGVECTOR_DSN is required")
		}
		t.Skip("GOLEM_TEST_PGVECTOR_DSN is not configured")
	}
	database, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	storage := "_golem_semantic_" + strings.ReplaceAll(t.Name(), "/", "_")
	if len(storage) > 48 {
		storage = storage[:48]
	}
	drop := func() {
		_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+pgvectorRankNamespace+`" CASCADE`)
	}
	drop()
	t.Cleanup(func() {
		drop()
		_ = database.Close()
	})
	for _, statement := range []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`CREATE SCHEMA "` + pgvectorRankNamespace + `"`,
		`CREATE TABLE "` + pgvectorRankNamespace + `"."docs" ("id" text NOT NULL PRIMARY KEY, "hidden" boolean NOT NULL)`,
		`CREATE TABLE "` + pgvectorRankNamespace + `"."` + storage + `_state" ("record_key" text NOT NULL PRIMARY KEY, "source_hash" bytea NOT NULL, "space_fingerprint" text NOT NULL, "status" text NOT NULL, "attempt_count" integer NOT NULL DEFAULT 0, "error_code" text, "updated_at" bigint NOT NULL, "id" text NOT NULL)`,
		`CREATE INDEX "` + storage + `_state_identity" ON "` + pgvectorRankNamespace + `"."` + storage + `_state" ("id")`,
		`CREATE TABLE "` + pgvectorRankNamespace + `"."` + storage + `_vec" ("record_key" text NOT NULL PRIMARY KEY, "embedding" vector(3) NOT NULL)`,
		`CREATE INDEX "` + storage + `_hnsw" ON "` + pgvectorRankNamespace + `"."` + storage + `_vec" USING hnsw ("embedding" vector_cosine_ops)`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	specification, err := embedding.NewSpecification("test", "deterministic", "v1", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{
		Descriptor: semanticstorage.Descriptor{
			ID: "rank-live", ModelID: "doc", Name: "related",
			Storage:  physical.PhysicalName(storage),
			Identity: []semanticstorage.IdentityColumn{{Name: "id", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, NotNull: true}},
		},
		Provider:      &deterministicProvider{specification: specification},
		Specification: specification,
	}
	manager := &Manager{
		database: database, provider: ir.PostgreSQL,
		schema:  physical.PhysicalSchema{Namespace: physical.Namespace{Name: pgvectorRankNamespace}},
		indexes: []Index{index},
	}
	return pgvectorRankFixture{database: database, manager: manager, index: index, storage: storage}
}

// seedRankRows inserts count rows whose cosine distance to [1,0,0] decreases
// strictly with the row ordinal. Nearest and last-by-record-key therefore
// coincide, so any statement that truncates the candidate set instead of
// ranking all of it loses exactly the rows the page must contain.
func (fixture pgvectorRankFixture) seedRankRows(t *testing.T, count int, hidden string) {
	t.Helper()
	ctx := context.Background()
	namespace := `"` + pgvectorRankNamespace + `".`
	if _, err := fixture.database.ExecContext(ctx, `INSERT INTO `+namespace+`"docs" ("id","hidden") SELECT 'k'||lpad(value::text,6,'0'), 'k'||lpad(value::text,6,'0')=$1 FROM generate_series(0,$2) value`, hidden, count-1); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.ExecContext(ctx, `INSERT INTO `+namespace+`"`+fixture.storage+`_state" ("record_key","source_hash","space_fingerprint","status","updated_at","id") SELECT 'k'||lpad(value::text,6,'0'), '\x01'::bytea, 'space-v1', 'ready', 1, 'k'||lpad(value::text,6,'0') FROM generate_series(0,$1) value`, count-1); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.ExecContext(ctx, `INSERT INTO `+namespace+`"`+fixture.storage+`_vec" ("record_key","embedding") SELECT 'k'||lpad(value::text,6,'0'), ('[1,'||(($1-value)::double precision/20)||',0]')::vector FROM generate_series(0,$1) value`, count-1); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.ExecContext(ctx, `ANALYZE `+namespace+`"`+fixture.storage+`_vec"`); err != nil {
		t.Fatal(err)
	}
}

func rankCandidates(statement string, args ...any) Candidates {
	return Candidates{
		SQL: statement, Args: args, Columns: []string{"id"},
		NewScan: func() IdentityScan { return &textIdentityScan{} },
	}
}

func authorizedRankCandidates() Candidates {
	return rankCandidates(`SELECT golem_r0."id" AS "id" FROM "`+pgvectorRankNamespace+`"."docs" AS golem_r0 WHERE golem_r0."hidden" = $1`, false)
}

// TestPGVectorRankHasNoCandidateCeilingAndRanksOnlyAuthorizedRows proves the
// authorized candidate set is no longer materialized in Go: 10,001 readable
// rows rank without refusal, and the globally nearest row stays out of both the
// result and the ordering because policy excludes it.
func TestPGVectorRankHasNoCandidateCeilingAndRanksOnlyAuthorizedRows(t *testing.T) {
	fixture := openPGVectorRankFixture(t)
	fixture.seedRankRows(t, 10002, "k009995")
	ranks, err := fixture.manager.rankVector(context.Background(), fixture.index, "[1,0,0]", authorizedRankCandidates(), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 20 {
		t.Fatalf("authorized ranks=%d want=20", len(ranks))
	}
	want := make([]string, 0, 20)
	for ordinal := 10001; len(want) < 20; ordinal-- {
		if ordinal == 9995 {
			continue
		}
		want = append(want, fmt.Sprintf("k%06d", ordinal))
	}
	for position, rank := range ranks {
		if rank.Key != want[position] {
			t.Fatalf("rank[%d]=%q want=%q", position, rank.Key, want[position])
		}
		if len(rank.Identity) != 1 || rank.Identity[0] != want[position] {
			t.Fatalf("rank[%d] identity=%#v want=%q", position, rank.Identity, want[position])
		}
	}
}

// TestPGVectorRankExcludesTheSimilaritySourceBeforeRanking pins that the source
// row leaves the neighbourhood inside the statement, so no predicate and no
// ordering can reintroduce it.
func TestPGVectorRankExcludesTheSimilaritySourceBeforeRanking(t *testing.T) {
	fixture := openPGVectorRankFixture(t)
	fixture.seedRankRows(t, 6, "")
	ranks, err := fixture.manager.rankVector(context.Background(), fixture.index, "[1,0,0]", authorizedRankCandidates(), "k000005", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 5 {
		t.Fatalf("similar ranks=%d want=5", len(ranks))
	}
	for position, rank := range ranks {
		if rank.Key == "k000005" {
			t.Fatalf("similarity source returned in its own results at %d", position)
		}
		if want := fmt.Sprintf("k%06d", 4-position); rank.Key != want {
			t.Fatalf("rank[%d]=%q want=%q", position, rank.Key, want)
		}
	}
}

// TestPGVectorRankIsExactAndNeverServedFromTheApproximateIndex asserts the
// pathkey breaker: the rank plan may not read the HNSW index, and its page must
// equal the exact bounded scan over the same authorized rows.
func TestPGVectorRankIsExactAndNeverServedFromTheApproximateIndex(t *testing.T) {
	fixture := openPGVectorRankFixture(t)
	fixture.seedRankRows(t, 10002, "k009995")
	ctx := context.Background()
	candidates := authorizedRankCandidates()
	statement := fixture.manager.rankStatement(fixture.index, candidates, false)
	arguments := append([]any{"[1,0,0]"}, candidates.Args...)
	arguments = append(arguments, 20)
	rows, err := fixture.database.QueryxContext(ctx, "EXPLAIN (COSTS OFF) "+statement, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.String(), fixture.storage+"_hnsw") {
		t.Fatalf("rank plan was served from the approximate HNSW index:\n%s", plan.String())
	}
	ranks, err := fixture.manager.rankVector(ctx, fixture.index, "[1,0,0]", candidates, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	exact := make([]string, 0, 20)
	if err := fixture.database.SelectContext(ctx, &exact, `SELECT v."record_key" FROM "`+pgvectorRankNamespace+`"."`+fixture.storage+`_vec" v JOIN "`+pgvectorRankNamespace+`"."docs" d ON d."id"=v."record_key" WHERE d."hidden"=false ORDER BY (v."embedding" <=> '[1,0,0]'::vector) + 0.0, v."record_key" COLLATE "C" LIMIT 20`); err != nil {
		t.Fatal(err)
	}
	if len(exact) != len(ranks) {
		t.Fatalf("exact page=%d ranked page=%d", len(exact), len(ranks))
	}
	for position := range exact {
		if exact[position] != ranks[position].Key {
			t.Fatalf("rank[%d]=%q exact=%q", position, ranks[position].Key, exact[position])
		}
	}
}

// TestPGVectorRankBreaksDistanceTiesInBinaryCollation runs on a linguistic
// database, where "a" sorts before "A". Ranking must still order the tie group
// by the binary collation used everywhere else in the read path.
func TestPGVectorRankBreaksDistanceTiesInBinaryCollation(t *testing.T) {
	fixture := openPGVectorRankFixture(t)
	ctx := context.Background()
	namespace := `"` + pgvectorRankNamespace + `".`
	for _, key := range []string{"a", "A"} {
		if _, err := fixture.database.ExecContext(ctx, `INSERT INTO `+namespace+`"docs" ("id","hidden") VALUES ($1,false)`, key); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.database.ExecContext(ctx, `INSERT INTO `+namespace+`"`+fixture.storage+`_state" ("record_key","source_hash","space_fingerprint","status","updated_at","id") VALUES ($1,'\x01'::bytea,'space-v1','ready',1,$1)`, key); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.database.ExecContext(ctx, `INSERT INTO `+namespace+`"`+fixture.storage+`_vec" ("record_key","embedding") VALUES ($1,'[1,0,0]'::vector)`, key); err != nil {
			t.Fatal(err)
		}
	}
	var linguistic []string
	if err := fixture.database.SelectContext(ctx, &linguistic, `SELECT "record_key" FROM `+namespace+`"`+fixture.storage+`_vec" ORDER BY "record_key"`); err != nil {
		t.Fatal(err)
	}
	if len(linguistic) != 2 || linguistic[0] != "a" {
		t.Skipf("database collation is not linguistic: %v", linguistic)
	}
	ranks, err := fixture.manager.rankVector(ctx, fixture.index, "[1,0,0]", authorizedRankCandidates(), "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 2 || ranks[0].Key != "A" || ranks[1].Key != "a" {
		t.Fatalf("tie order=%#v want binary collation A,a", ranks)
	}
}
