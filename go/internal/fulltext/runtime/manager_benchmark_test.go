package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

func BenchmarkSQLiteFullTextRead200K(b *testing.B) {
	database, _, err := sqliteprovider.New().Open(context.Background(), filepath.Join(b.TempDir(), "fulltext-read.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY, allowed INTEGER NOT NULL) STRICT`,
		`CREATE INDEX docs_allowed ON docs(allowed)`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,_golem_field_1,_golem_field_2,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
		`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<200000) INSERT INTO docs SELECT printf('%06d',n),n%1000=0 FROM seq`,
		`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<200000) INSERT INTO _golem_fulltext_x_keys SELECT n,printf('%06d',n) FROM seq`,
	} {
		if _, err := database.Exec(statement); err != nil {
			b.Fatal(err)
		}
	}
	filler := strings.Repeat(" filler", 280)
	if _, err := database.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<200000) INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0,_golem_field_1,_golem_field_2) SELECT n,(CASE WHEN n<=100 THEN 'rare ' ELSE '' END)||(CASE WHEN n<=10000 THEN 'alpha ' ELSE '' END)||'mail subject',(CASE WHEN n<=1000 THEN 'kilo beta ' ELSE '' END)||'sender recipient',(CASE WHEN n<=10000 THEN 'tenk ' ELSE '' END)||(CASE WHEN n<=100 THEN 'gamma ' ELSE '' END)||'universal'||? FROM seq`, filler); err != nil {
		b.Fatal(err)
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "subject", Weight: 3}, {ID: "participants", Weight: 2}, {ID: "body", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	for _, benchmark := range []struct {
		name, query, candidateSQL string
		args                      []any
	}{
		{name: "one-term-100", query: "rare", candidateSQL: `SELECT id FROM docs`},
		{name: "one-term-1000", query: "kilo", candidateSQL: `SELECT id FROM docs`},
		{name: "one-term-10000", query: "tenk", candidateSQL: `SELECT id FROM docs`},
		{name: "three-terms", query: "alpha beta gamma", candidateSQL: `SELECT id FROM docs`},
		{name: "selective-0.1-percent", query: "tenk", candidateSQL: `SELECT id FROM docs WHERE allowed=?1`, args: []any{1}},
		{name: "common-term", query: "universal", candidateSQL: `SELECT id FROM docs`},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			candidates := semanticruntime.Candidates{SQL: benchmark.candidateSQL, Args: benchmark.args, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: readsql.MaxStatementParameters, MaxStatementBytes: readsql.MaxStatementBytes, MaxStatementAliases: readsql.MaxStatementAliases, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
			b.ResetTimer()
			for b.Loop() {
				if _, err := manager.Query(context.Background(), "m", "content", benchmark.query, candidates, 20); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
