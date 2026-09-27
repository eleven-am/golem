package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextpostgresql "github.com/eleven-am/golem/go/internal/fulltext/postgresql"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	postgresqlprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
	"github.com/eleven-am/golem/go/internal/testenv"
)

type stringScan struct {
	value string
}

func (scan *stringScan) Destinations() []any { return []any{&scan.value} }
func (scan *stringScan) RawValues() []any    { return []any{scan.value} }

func TestSQLiteQueryIsAuthorizedRankedAndLiteral(t *testing.T) {
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "fulltext.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	db := database
	for _, statement := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY, allowed INTEGER NOT NULL, title TEXT NOT NULL) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(title,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 2')`,
		`INSERT INTO docs VALUES('a',1,'Renée alpha'),('b',0,'alpha alpha alpha'),('c',1,'literal OR token')`,
		`INSERT INTO _golem_fulltext_x_keys(docid,id) VALUES(1,'a'),(2,'b'),(3,'c')`,
		`INSERT INTO _golem_fulltext_x_fts(rowid,title) VALUES(1,'Renée alpha'),(2,'alpha alpha alpha'),(3,'literal OR token')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{database: db, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingDiacritics, Fields: []fulltextcontract.Field{{ID: "title", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs WHERE allowed=?1`, Args: []any{1}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	ranks, err := manager.Query(context.Background(), "m", "content", "alpha", candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 1 || ranks[0].Identity[0] != "a" {
		t.Fatalf("authorized ranks=%#v", ranks)
	}
	ranks, err = manager.Query(context.Background(), "m", "content", `OR`, candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 1 || ranks[0].Identity[0] != "c" {
		t.Fatalf("literal operator ranks=%#v", ranks)
	}
	ranks, err = manager.Query(context.Background(), "m", "content", "renee", candidates, 10)
	if err != nil || len(ranks) != 1 || ranks[0].Identity[0] != "a" {
		t.Fatalf("folded ranks=%#v err=%v", ranks, err)
	}
}

func TestPostgreSQLQueryIsAuthorizedRankedAndPortable(t *testing.T) {
	dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
	database, _, err := postgresqlprovider.New().Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	const namespace = "golem_fulltext_runtime_live"
	_, _ = database.Exec(`DROP SCHEMA IF EXISTS "golem_fulltext_runtime_live" CASCADE`)
	defer database.Exec(`DROP SCHEMA IF EXISTS "golem_fulltext_runtime_live" CASCADE`)
	for _, statement := range []string{
		`CREATE EXTENSION IF NOT EXISTS unaccent WITH SCHEMA public`,
		`CREATE SCHEMA "golem_fulltext_runtime_live"`,
		`CREATE TABLE "golem_fulltext_runtime_live"."docs" ("id" text PRIMARY KEY,"allowed" boolean NOT NULL,"title" text NOT NULL)`,
		`CREATE TABLE "golem_fulltext_runtime_live"."_golem_fulltext_x_fts" ("id" text PRIMARY KEY,"document" tsvector NOT NULL)`,
		`INSERT INTO "golem_fulltext_runtime_live"."docs" VALUES ('a',true,'Renée alpha invoice.pdf Καφές 東京'),('b',false,'alpha alpha alpha'),('c',true,'literal OR token')`,
		`INSERT INTO "golem_fulltext_runtime_live"."_golem_fulltext_x_fts" SELECT "id",setweight(to_tsvector('simple',` + fulltextpostgresql.NormalizeText(`"title"`, fulltextcontract.FoldingDiacritics) + `),'A') FROM "golem_fulltext_runtime_live"."docs"`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{
		database: database,
		provider: ir.PostgreSQL,
		schema:   physical.PhysicalSchema{Namespace: physical.Namespace{Name: namespace}},
		indexes: []Index{{
			Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingDiacritics, Fields: []fulltextcontract.Field{{ID: "title", Weight: 1}}}},
			Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}}},
		}},
	}
	candidates := semanticruntime.Candidates{SQL: `SELECT "id" FROM "golem_fulltext_runtime_live"."docs" WHERE "allowed"=$1`, Args: []any{true}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	for _, query := range []string{"alpha", "renee", `invoice.pdf`, `"invoice pdf"`, "invoice.pdf*", "inv*", "Καφές", "東京", "OR"} {
		ranks, err := manager.Query(context.Background(), "m", "content", query, candidates, 10)
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		want := "a"
		if query == "OR" {
			want = "c"
		}
		if len(ranks) != 1 || ranks[0].Identity[0] != want {
			t.Fatalf("query %q ranks=%#v", query, ranks)
		}
	}
}

func TestQueryParserRejectsUnsafeShapes(t *testing.T) {
	for _, query := range []string{"", `"unterminated`, "x*", string([]byte{'a', 0, 'b'})} {
		if _, err := parse(query); err == nil {
			t.Fatalf("query %q accepted", query)
		}
	}
	query := ""
	for index := 0; index < 33; index++ {
		query += " word"
	}
	if _, err := parse(query); err == nil {
		t.Fatal("33 terms accepted")
	}
}
