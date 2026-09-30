package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
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

var punctuatedPhraseDocuments = [][2]string{{"a", "contact user@example.com today"}, {"b", "user@example.community"}, {"c", "user example commander"}, {"d", "user_name"}, {"e", "user nameless"}, {"f", "username"}, {"g", "user example com"}, {"h", "example user com"}}
var punctuatedPhraseQueries = map[string]string{
	`"user@example.com"*`: "a,b,c,g",
	`"user@example.co"*`:  "a,b,c,g",
	`user@example.com*`:   "a,b,c,g",
	`"user@example.com"`:  "a,g",
	`"user_name"*`:        "d,e",
	`user_name`:           "d",
}

func TestPunctuatedPhrasePrefixMatchesTheSameRowsOnEveryProvider(t *testing.T) {
	sqliteManager, sqliteCandidates := punctuatedPhraseSQLite(t)
	for _, variable := range []string{testenv.PostgreSQLDSNVariable, testenv.LinguisticDSNVariable} {
		postgresManager, postgresCandidates := punctuatedPhrasePostgreSQL(t, variable)
		for query, want := range punctuatedPhraseQueries {
			sqliteRows := punctuatedPhraseRows(t, sqliteManager, sqliteCandidates, query)
			postgresRows := punctuatedPhraseRows(t, postgresManager, postgresCandidates, query)
			if sqliteRows != want || postgresRows != want {
				t.Errorf("%s query %s: sqlite=[%s] postgresql=[%s] want=[%s]", variable, query, sqliteRows, postgresRows, want)
			}
		}
	}
}

func punctuatedPhraseRows(t *testing.T, manager *Manager, candidates semanticruntime.Candidates, query string) string {
	t.Helper()
	ranks, err := manager.Query(context.Background(), "m", "content", query, candidates, 20)
	if err != nil {
		t.Fatalf("query %s: %v", query, err)
	}
	ids := make([]string, len(ranks))
	for index, rank := range ranks {
		ids[index] = fmt.Sprint(rank.Identity[0])
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func punctuatedPhraseSQLite(t *testing.T) (*Manager, semanticruntime.Candidates) {
	t.Helper()
	provider := sqliteprovider.New()
	sdb, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "fulltext.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	for _, s := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY, allowed INTEGER NOT NULL, title TEXT NOT NULL) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
	} {
		if _, err := sdb.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	for i, d := range punctuatedPhraseDocuments {
		sdb.MustExec(`INSERT INTO docs VALUES(?,1,?)`, d[0], d[1])
		sdb.MustExec(`INSERT INTO _golem_fulltext_x_keys VALUES(?,?)`, i+1, d[0])
		sdb.MustExec(`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0) VALUES(?,?)`, i+1, d[1])
	}
	sm := &Manager{database: sdb, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Fields: []fulltextcontract.Field{{ID: "title", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	sc := semanticruntime.Candidates{SQL: `SELECT id FROM docs WHERE allowed=?1`, Args: []any{1}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 100, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	return sm, sc
}

func punctuatedPhrasePostgreSQL(t *testing.T, variable string) (*Manager, semanticruntime.Candidates) {
	t.Helper()
	pdb, _, err := postgresqlprovider.New().Open(context.Background(), testenv.DisposablePostgreSQL(t, variable))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Close() })
	for _, s := range []string{`CREATE SCHEMA "punctuated_phrase"`, `CREATE TABLE "punctuated_phrase"."docs" ("id" text PRIMARY KEY,"allowed" boolean NOT NULL,"title" text NOT NULL)`, `CREATE TABLE "punctuated_phrase"."_golem_fulltext_x_fts" ("id" text PRIMARY KEY,"document" tsvector NOT NULL)`} {
		pdb.MustExec(s)
	}
	for _, d := range punctuatedPhraseDocuments {
		pdb.MustExec(`INSERT INTO "punctuated_phrase"."docs" VALUES ($1,true,$2)`, d[0], d[1])
	}
	pdb.MustExec(`INSERT INTO "punctuated_phrase"."_golem_fulltext_x_fts" SELECT "id",setweight(to_tsvector('simple',` + fulltextpostgresql.NormalizeText(`"title"`, fulltextcontract.FoldingNone) + `),'A') FROM "punctuated_phrase"."docs"`)
	pm := &Manager{database: pdb, provider: ir.PostgreSQL, schema: physical.PhysicalSchema{Namespace: physical.Namespace{Name: "punctuated_phrase"}}, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Fields: []fulltextcontract.Field{{ID: "title", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}}},
	}}}
	pc := semanticruntime.Candidates{SQL: `SELECT "id" FROM "punctuated_phrase"."docs" WHERE "allowed"=$1`, Args: []any{true}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 100, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	return pm, pc
}
