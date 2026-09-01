package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
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
		Provider:         &deterministicProvider{specification: specification},
		Specification:    specification,
		SpaceFingerprint: sha256.Sum256([]byte(specification.FingerprintInput())),
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
	if _, err := fixture.database.ExecContext(ctx, `INSERT INTO `+namespace+`"`+fixture.storage+`_state" ("record_key","source_hash","space_fingerprint","status","updated_at","id") SELECT 'k'||lpad(value::text,6,'0'), '\x01'::bytea, $2, 'ready', 1, 'k'||lpad(value::text,6,'0') FROM generate_series(0,$1) value`, count-1, hex.EncodeToString(fixture.index.SpaceFingerprint[:])); err != nil {
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
		MaxStatementBytes: readsql.MaxStatementBytes, MaxStatementAliases: readsql.MaxStatementAliases,
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
	arguments = append(arguments, hex.EncodeToString(fixture.index.SpaceFingerprint[:]))
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
		if _, err := fixture.database.ExecContext(ctx, `INSERT INTO `+namespace+`"`+fixture.storage+`_state" ("record_key","source_hash","space_fingerprint","status","updated_at","id") VALUES ($1,'\x01'::bytea,$2,'ready',1,$1)`, key, hex.EncodeToString(fixture.index.SpaceFingerprint[:])); err != nil {
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

type pgvectorDrainFixture struct {
	database *sqlx.DB
	manager  *Manager
	embedder *deterministicProvider
	storage  string
}

func openPGVectorDrainFixture(t *testing.T) pgvectorDrainFixture {
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
	storage := "_golem_semantic_drain_live"
	drop := func() {
		_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+pgvectorRankNamespace+`" CASCADE`)
	}
	drop()
	t.Cleanup(func() {
		drop()
		_ = database.Close()
	})
	namespace := `"` + pgvectorRankNamespace + `".`
	for _, statement := range []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`CREATE SCHEMA "` + pgvectorRankNamespace + `"`,
		`CREATE TABLE ` + namespace + `"docs" ("id" text NOT NULL PRIMARY KEY, "title" text)`,
		`CREATE TABLE ` + namespace + `"` + storage + `_state" ("record_key" text NOT NULL PRIMARY KEY, "source_hash" bytea NOT NULL, "space_fingerprint" text NOT NULL, "status" text NOT NULL CHECK ("status" IN ('pending','ready','failed')), "attempt_count" integer NOT NULL DEFAULT 0, "error_code" text, "updated_at" bigint NOT NULL, "id" text NOT NULL)`,
		`CREATE INDEX "` + storage + `_state_identity" ON ` + namespace + `"` + storage + `_state" ("id")`,
		`CREATE INDEX "` + storage + `_state_stale" ON ` + namespace + `"` + storage + `_state" ("record_key") WHERE "status" <> 'ready'`,
		`CREATE TABLE ` + namespace + `"` + storage + `_vec" ("record_key" text NOT NULL PRIMARY KEY, "embedding" vector(3) NOT NULL)`,
		`INSERT INTO ` + namespace + `"docs" ("id","title") VALUES ('a','alpha'),('b','beta')`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	specification, err := embedding.NewSpecification("test", "deterministic", "v1", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &deterministicProvider{specification: specification}
	index := Index{
		Descriptor: semanticstorage.Descriptor{
			ID: "drain-live", ModelID: "doc", Name: "related",
			Storage:  physical.PhysicalName(storage),
			Fields:   []ir.FieldID{"doc-title"},
			Identity: []semanticstorage.IdentityColumn{{Name: "id", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, NotNull: true}},
		},
		Provider:      embedder,
		Specification: specification,
	}
	manager := &Manager{
		database: database, provider: ir.PostgreSQL,
		schema: physical.PhysicalSchema{
			Namespace: physical.Namespace{Name: pgvectorRankNamespace},
			Tables: []physical.PhysicalTable{{
				ID: "doc", Name: "docs",
				Columns: []physical.PhysicalColumn{
					{ID: "doc-id", Name: "id", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}},
					{ID: "doc-title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}},
				},
				PrimaryKey: &physical.PhysicalKey{ID: "doc-primary", Name: "pk_docs", Columns: []ir.FieldID{"doc-id"}},
			}},
		},
		indexes: []Index{index},
	}
	if err := manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if embedder.calls() != 2 {
		t.Fatalf("seed embedded=%d want=2", embedder.calls())
	}
	return pgvectorDrainFixture{database: database, manager: manager, embedder: embedder, storage: storage}
}

func (fixture pgvectorDrainFixture) mark(t *testing.T, id string) {
	t.Helper()
	key, err := semantickey.Encode([]any{id})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.database.Exec(`UPDATE "`+pgvectorRankNamespace+`"."`+fixture.storage+`_state" SET status='pending' WHERE record_key=$1`, key)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("mark %q affected=%d", id, affected)
	}
}

func (fixture pgvectorDrainFixture) statusOf(t *testing.T, id string) string {
	t.Helper()
	key, err := semantickey.Encode([]any{id})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := fixture.database.Get(&status, `SELECT status FROM "`+pgvectorRankNamespace+`"."`+fixture.storage+`_state" WHERE record_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	return status
}

// TestPGVectorDrainAdvancesOnlyMarkedRecords runs the whole incremental path
// against real PostgreSQL: the stale probe, the identity join back to the owner
// table, the unchanged flip, and the owner-absent cleanup all render in the
// PostgreSQL dialect and none of them reads the owner table as a whole.
func TestPGVectorDrainAdvancesOnlyMarkedRecords(t *testing.T) {
	ctx := context.Background()
	fixture := openPGVectorDrainFixture(t)
	namespace := `"` + pgvectorRankNamespace + `".`

	fixture.mark(t, "a")
	pending, err := fixture.manager.Drain(ctx, "doc", "related")
	if err != nil {
		t.Fatal(err)
	}
	if pending || fixture.embedder.calls() != 2 || fixture.statusOf(t, "a") != "ready" {
		t.Fatalf("unchanged mark pending=%t calls=%d status=%q", pending, fixture.embedder.calls(), fixture.statusOf(t, "a"))
	}

	if _, err := fixture.database.ExecContext(ctx, `UPDATE `+namespace+`"docs" SET title='alpha revised' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.ExecContext(ctx, `UPDATE `+namespace+`"docs" SET title='beta revised' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	fixture.mark(t, "a")
	if _, err := fixture.manager.Drain(ctx, "doc", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.embedder.calls() != 3 {
		t.Fatalf("drain embedded=%d want=3 (only the marked record)", fixture.embedder.calls())
	}
	if got := fixture.embedder.texts()[2]; !strings.Contains(got, "alpha revised") {
		t.Fatalf("drain embedded %q", got)
	}
	if fixture.statusOf(t, "b") != "ready" {
		t.Fatalf("unmarked record status=%q", fixture.statusOf(t, "b"))
	}

	fixture.mark(t, "b")
	if _, err := fixture.database.ExecContext(ctx, `DELETE FROM `+namespace+`"docs" WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.Drain(ctx, "doc", "related"); err != nil {
		t.Fatal(err)
	}
	var states, vectors int
	if err := fixture.database.Get(&states, `SELECT count(*) FROM `+namespace+`"`+fixture.storage+`_state"`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.Get(&vectors, `SELECT count(*) FROM `+namespace+`"`+fixture.storage+`_vec"`); err != nil {
		t.Fatal(err)
	}
	if states != 1 || vectors != 1 {
		t.Fatalf("owner-absent cleanup states=%d vectors=%d want=1,1", states, vectors)
	}
	if fixture.embedder.calls() != 3 {
		t.Fatalf("owner removal embedded: calls=%d", fixture.embedder.calls())
	}
}

// TestPGVectorMarkStaleEmbedsBrandNewRecordsOfEveryIdentityKind drives EVERY
// identity storage kind the Manager will convert, in one composite key, through
// the real join. That is the point: the drain reaches owner rows by joining the
// mirrored identity columns, so a value in the wrong physical representation
// joins nothing, is classified as an absent owner, and has its vector deleted.
// Asserting the record was embedded is what rejects that, per kind.
func TestPGVectorMarkStaleEmbedsBrandNewRecordsOfEveryIdentityKind(t *testing.T) {
	ctx := context.Background()
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
	storage := "_golem_semantic_mark_live"
	drop := func() { _, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+pgvectorRankNamespace+`" CASCADE`) }
	drop()
	t.Cleanup(func() { drop(); _ = database.Close() })
	namespace := `"` + pgvectorRankNamespace + `".`
	// Sub-microsecond nanoseconds on purpose: PostgreSQL stores microseconds, so
	// a key derived from the caller's untruncated value would not be the key the
	// refresh scan reads back.
	instant := time.Date(2026, 3, 4, 5, 6, 7, 891011567, time.UTC)
	identityColumns := `"id" uuid NOT NULL, "serial" bigint NOT NULL, "ordinal" integer NOT NULL, "shard" smallint NOT NULL, "tag" bytea NOT NULL, "region" varchar(16) NOT NULL, "active" boolean NOT NULL, "at" timestamptz NOT NULL`
	for _, statement := range []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`CREATE SCHEMA "` + pgvectorRankNamespace + `"`,
		`CREATE TABLE ` + namespace + `"docs" (` + identityColumns + `, "title" text, PRIMARY KEY ("id","serial","ordinal","shard","tag","region","active","at"))`,
		`CREATE TABLE ` + namespace + `"` + storage + `_state" ("record_key" text NOT NULL PRIMARY KEY, "source_hash" bytea NOT NULL, "space_fingerprint" text NOT NULL, "status" text NOT NULL, "attempt_count" integer NOT NULL DEFAULT 0, "error_code" text, "updated_at" bigint NOT NULL, ` + identityColumns + `)`,
		`CREATE INDEX "` + storage + `_state_stale" ON ` + namespace + `"` + storage + `_state" ("record_key") WHERE "status" <> 'ready'`,
		`CREATE TABLE ` + namespace + `"` + storage + `_vec" ("record_key" text NOT NULL PRIMARY KEY, "embedding" vector(3) NOT NULL)`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO `+namespace+`"docs" ("id","serial","ordinal","shard","tag","region","active","at","title") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		"10000000-0000-0000-0000-000000000001", int64(7), int64(9), int64(3), []byte{0xde, 0xad}, "eu-west", true, instant, "alpha doc"); err != nil {
		t.Fatal(err)
	}
	specification, err := embedding.NewSpecification("test", "deterministic", "v1", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &deterministicProvider{specification: specification}
	kinds := []struct {
		name    physical.PhysicalName
		field   ir.FieldID
		storage physical.StorageKind
	}{
		{"id", "doc-id", physical.StoragePostgreSQLUUID},
		{"serial", "doc-serial", physical.StoragePostgreSQLBigInt},
		{"ordinal", "doc-ordinal", physical.StoragePostgreSQLInteger},
		{"shard", "doc-shard", physical.StoragePostgreSQLSmallInt},
		{"tag", "doc-tag", physical.StoragePostgreSQLBytea},
		{"region", "doc-region", physical.StoragePostgreSQLVarchar},
		{"active", "doc-active", physical.StoragePostgreSQLBoolean},
		{"at", "doc-at", physical.StoragePostgreSQLTimestampTZ},
	}
	columns := make([]physical.PhysicalColumn, 0, len(kinds)+1)
	identity := make([]semanticstorage.IdentityColumn, 0, len(kinds))
	keyFields := make([]ir.FieldID, 0, len(kinds))
	for ordinal, kind := range kinds {
		columns = append(columns, physical.PhysicalColumn{ID: kind.field, Name: kind.name, Ordinal: uint32(ordinal), Storage: physical.StorageType{Kind: kind.storage}})
		identity = append(identity, semanticstorage.IdentityColumn{Name: kind.name, Storage: physical.StorageType{Kind: kind.storage}, NotNull: true})
		keyFields = append(keyFields, kind.field)
	}
	columns = append(columns, physical.PhysicalColumn{ID: "doc-title", Name: "title", Ordinal: uint32(len(kinds)), Nullable: true, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}})
	table := physical.PhysicalTable{
		ID: "doc", Name: "docs", Columns: columns,
		PrimaryKey: &physical.PhysicalKey{ID: "doc-primary", Name: "pk_docs", Columns: keyFields},
	}
	index := Index{
		Descriptor: semanticstorage.Descriptor{
			ID: "mark-live", ModelID: "doc", Name: "related",
			Storage: physical.PhysicalName(storage), Fields: []ir.FieldID{"doc-title"}, Identity: identity,
		},
		Provider: embedder, Specification: specification, SpaceFingerprint: [32]byte{7},
	}
	manager := &Manager{
		database: database, provider: ir.PostgreSQL,
		schema:  physical.PhysicalSchema{Namespace: physical.Namespace{Name: pgvectorRankNamespace}, Tables: []physical.PhysicalTable{table}},
		indexes: []Index{index},
	}
	uuidValue := [16]byte{0x10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01}
	logical := []any{uuidValue, int64(7), int64(9), int64(3), []byte{0xde, 0xad}, "eu-west", true, instant}
	stored := []any{uuidValue, int64(7), int64(9), int64(3), []byte{0xde, 0xad}, "eu-west", true, instant.Truncate(time.Microsecond)}
	key, err := semantickey.Encode(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkStale(ctx, database, "doc", semanticMarkBinds, []MarkRecord{{Key: key, Identity: logical}}); err != nil {
		t.Fatal(err)
	}
	scanned, err := manager.scanSources(ctx, table, index)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) != 1 || scanned[0].key != key {
		t.Fatalf("mark stored key=%q, refresh scan produces %d rows keyed %q", key, len(scanned), scanned[0].key)
	}
	if _, err := manager.Drain(ctx, "doc", "related"); err != nil {
		t.Fatal(err)
	}
	if embedder.calls() != 1 {
		t.Fatalf("brand-new marked record embedded=%d want=1 (0 means it was flipped to ready or deleted as an absent owner)", embedder.calls())
	}
	var ready, vectors int
	if err := database.Get(&ready, `SELECT count(*) FROM `+namespace+`"`+storage+`_state" WHERE status='ready'`); err != nil {
		t.Fatal(err)
	}
	if err := database.Get(&vectors, `SELECT count(*) FROM `+namespace+`"`+storage+`_vec"`); err != nil {
		t.Fatal(err)
	}
	if ready != 1 || vectors != 1 {
		t.Fatalf("after drain ready=%d vectors=%d want=1,1", ready, vectors)
	}
}

func (fixture pgvectorDrainFixture) markStale(t *testing.T, ids ...string) {
	t.Helper()
	records := make([]MarkRecord, 0, len(ids))
	for _, id := range ids {
		key, err := semantickey.Encode([]any{id})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, MarkRecord{Key: key, Identity: []any{id}})
	}
	if err := fixture.manager.MarkStale(context.Background(), fixture.database, "doc", semanticMarkBinds, records); err != nil {
		t.Fatal(err)
	}
}

func (fixture pgvectorDrainFixture) stateOf(t *testing.T, id string) (string, sql.NullString, []byte) {
	t.Helper()
	key, err := semantickey.Encode([]any{id})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var code sql.NullString
	var hash []byte
	if err := fixture.database.QueryRow(`SELECT status,error_code,source_hash FROM "`+pgvectorRankNamespace+`"."`+fixture.storage+`_state" WHERE record_key=$1`, key).Scan(&status, &code, &hash); err != nil {
		t.Fatal(err)
	}
	return status, code, hash
}

// TestPGVectorDrainKeepsAMarkOnTheRecordItIsEmbedding renders the conditional
// upsert in the PostgreSQL dialect and proves it discriminates: PostgreSQL and
// SQLite spell the guarded conflict arm differently, so a dialect that accepted
// the statement but ignored the predicate would pass every SQLite gate.
func TestPGVectorDrainKeepsAMarkOnTheRecordItIsEmbedding(t *testing.T) {
	ctx := context.Background()
	fixture := openPGVectorDrainFixture(t)
	namespace := `"` + pgvectorRankNamespace + `".`
	fired := false
	fixture.embedder.database = fixture.database
	fixture.embedder.beforeEmbed = func(*sqlx.DB) {
		if fired {
			return
		}
		fired = true
		if _, err := fixture.database.ExecContext(ctx, `UPDATE `+namespace+`"docs" SET title='alpha third' WHERE id='a'`); err != nil {
			t.Error(err)
			return
		}
		fixture.markStale(t, "a")
	}
	if _, err := fixture.database.ExecContext(ctx, `UPDATE `+namespace+`"docs" SET title='alpha revised' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "a")
	_, _, before := fixture.stateOf(t, "a")
	pending, err := fixture.manager.Drain(ctx, "doc", "related")
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the mid-pass mark never ran")
	}
	status, _, after := fixture.stateOf(t, "a")
	if status != "pending" || !equalBytes(before, after) || !pending {
		t.Fatalf("the pass erased a mark on the record it was embedding: status=%q hash unchanged=%t pending=%t", status, equalBytes(before, after), pending)
	}
	if _, err := fixture.manager.Drain(ctx, "doc", "related"); err != nil {
		t.Fatal(err)
	}
	if status, _, latest := fixture.stateOf(t, "a"); status != "ready" || equalBytes(before, latest) {
		t.Fatalf("chained pass status=%q hash unchanged=%t", status, equalBytes(before, latest))
	}
	texts := fixture.embedder.texts()
	if !strings.Contains(texts[len(texts)-1], "alpha third") {
		t.Fatalf("chained pass embedded %q", texts[len(texts)-1])
	}
}

// TestPGVectorDrainQuarantinesTheDocumentAProviderRefuses runs the quarantine
// against the real CHECK constraint that declares the failed status, and
// against a real bytea state row.
func TestPGVectorDrainQuarantinesTheDocumentAProviderRefuses(t *testing.T) {
	ctx := context.Background()
	fixture := openPGVectorDrainFixture(t)
	namespace := `"` + pgvectorRankNamespace + `".`
	const poisoned = "p02"
	ids := make([]string, 0, 12)
	for ordinal := range 12 {
		id := fmt.Sprintf("p%02d", ordinal)
		title := "gamma " + id
		if id == poisoned {
			title = "poison " + id
		}
		if _, err := fixture.database.ExecContext(ctx, `INSERT INTO `+namespace+`"docs" ("id","title") VALUES ($1,$2)`, id, title); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	fixture.markStale(t, ids...)
	fixture.embedder.rejectText = "poison"
	seeded := fixture.embedder.calls()
	pending, err := fixture.manager.Drain(ctx, "doc", "related")
	if err != nil {
		t.Fatalf("one refused document failed the whole pass: %v", err)
	}
	if pending {
		t.Fatal("a quarantined record kept the drain chaining a successor for work no pass can finish")
	}
	if got := fixture.embedder.calls() - seeded; got != len(ids)-1 {
		t.Fatalf("embedded=%d want=%d", got, len(ids)-1)
	}
	for _, id := range ids {
		if id == poisoned {
			continue
		}
		if status, _, _ := fixture.stateOf(t, id); status != "ready" {
			t.Fatalf("record %q status=%q — the refusal was not contained to the document that caused it", id, status)
		}
	}
	status, code, _ := fixture.stateOf(t, poisoned)
	if status != "failed" || !code.Valid || code.String == "" {
		t.Fatalf("quarantined record status=%q error_code=%#v", status, code)
	}
	var attempts int
	key, err := semantickey.Encode([]any{poisoned})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.Get(&attempts, `SELECT attempt_count FROM `+namespace+`"`+fixture.storage+`_state" WHERE record_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("quarantined record attempt_count=%d want=1", attempts)
	}
	if pending, err = fixture.manager.Drain(ctx, "doc", "related"); err != nil || pending {
		t.Fatalf("second pass pending=%t error=%v", pending, err)
	}
	if got := fixture.embedder.calls() - seeded; got != len(ids)-1 {
		t.Fatalf("a quarantined record was sent to the provider again: embedded=%d", got)
	}
	if _, err := fixture.database.ExecContext(ctx, `UPDATE `+namespace+`"docs" SET title='gamma p02' WHERE id=$1`, poisoned); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, poisoned)
	if _, err := fixture.manager.Drain(ctx, "doc", "related"); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := fixture.stateOf(t, poisoned); status != "ready" {
		t.Fatalf("a write to a quarantined record did not retry it: status=%q", status)
	}
}

func TestPGVectorSourceVectorSeparatesAbsentFailedAndEmpty(t *testing.T) {
	fixture := openPGVectorRankFixture(t)
	fixture.seedRankRows(t, 4, "")
	ctx := context.Background()
	vectors := `"` + pgvectorRankNamespace + `"."` + fixture.storage + `_vec"`

	if _, err := fixture.manager.sourceVector(ctx, fixture.index, "k000000"); err != nil {
		t.Fatalf("an embedded source did not resolve: %v", err)
	}
	_, absent := fixture.manager.sourceVector(ctx, fixture.index, "k999999")
	if absent == nil || absent.Error() != "P9_SEMANTIC_QUERY: semantic source vector is unavailable" {
		t.Fatalf("never embedded source: %v", absent)
	}

	if _, err := fixture.database.ExecContext(ctx, `ALTER TABLE `+vectors+` DROP COLUMN "embedding"`); err != nil {
		t.Fatal(err)
	}
	_, failed := fixture.manager.sourceVector(ctx, fixture.index, "k000000")
	if failed == nil || failed.Error() != "P9_SEMANTIC_QUERY: semantic source vector read failed" {
		t.Fatalf("storage read failure: %v", failed)
	}
	if failed.Error() == absent.Error() {
		t.Fatal("a storage read failure reports as if the source was never embedded")
	}
	for _, leak := range []string{"k000000", fixture.storage, "SELECT", "embedding", "column", "pq:", "SQLSTATE"} {
		if strings.Contains(failed.Error(), leak) {
			t.Fatalf("source vector failure %q discloses %q", failed.Error(), leak)
		}
	}

	if _, err := fixture.database.ExecContext(ctx, `ALTER TABLE `+vectors+` ADD COLUMN "embedding" text NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	_, empty := fixture.manager.sourceVector(ctx, fixture.index, "k000000")
	if empty == nil || empty.Error() != "P9_SEMANTIC_QUERY: semantic source vector is empty" {
		t.Fatalf("empty stored vector: %v", empty)
	}
}
