package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

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
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
		`INSERT INTO docs VALUES('a',1,'Renée alpha Καφές Łódź'),('b',0,'alpha alpha alpha'),('c',1,'literal OR token')`,
		`INSERT INTO _golem_fulltext_x_keys(docid,id) VALUES(1,'a'),(2,'b'),(3,'c')`,
		`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0) SELECT 1,golem_fulltext_fold('Renée alpha Καφές Łódź') UNION ALL SELECT 2,golem_fulltext_fold('alpha alpha alpha') UNION ALL SELECT 3,golem_fulltext_fold('literal OR token')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{database: db, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingDiacritics, Fields: []fulltextcontract.Field{{ID: "title", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs WHERE allowed=?1`, Args: []any{1}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 100, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
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
	ranks, err = manager.Query(context.Background(), "m", "content", "Καφές", candidates, 10)
	if err != nil || len(ranks) != 1 || ranks[0].Identity[0] != "a" {
		t.Fatalf("cross-script folded ranks=%#v err=%v", ranks, err)
	}
	ranks, err = manager.Query(context.Background(), "m", "content", "Łodz", candidates, 10)
	if err != nil || len(ranks) != 1 || ranks[0].Identity[0] != "a" {
		t.Fatalf("canonical folded ranks=%#v err=%v", ranks, err)
	}
	ranks, err = manager.Query(context.Background(), "m", "content", "Lodz", candidates, 10)
	if err != nil || len(ranks) != 0 {
		t.Fatalf("non-diacritic transliteration ranks=%#v err=%v", ranks, err)
	}
}

func TestQueryOnUsesTransactionBoundExecutor(t *testing.T) {
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "fulltext-tx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "title", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs`, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 100, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	tx, err := database.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`INSERT INTO docs VALUES('pending')`,
		`INSERT INTO _golem_fulltext_x_keys VALUES(1,'pending')`,
		`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0) VALUES(1,'alpha')`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ranks, err := manager.QueryOn(context.Background(), tx, "m", "content", "alpha", candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 1 || ranks[0].Identity[0] != "pending" {
		t.Fatalf("transaction ranks=%#v", ranks)
	}
}

func TestSQLiteRankingIgnoresUnauthorizedCorpus(t *testing.T) {
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "fulltext-policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY, allowed INTEGER NOT NULL) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
		`INSERT INTO docs VALUES('a',1),('b',1)`,
		`INSERT INTO _golem_fulltext_x_keys VALUES(1,'a'),(2,'b')`,
		`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0) VALUES(1,'alpha'),(2,'beta')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "title", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs WHERE allowed=?1`, Args: []any{1}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 1000, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	before, err := manager.Query(context.Background(), "m", "content", "alpha beta", candidates, 1)
	if err != nil || len(before) != 1 || before[0].Identity[0] != "a" {
		t.Fatalf("initial ranks=%#v err=%v", before, err)
	}
	for index := 0; index < 100; index++ {
		key := "hidden-" + strconv.Itoa(index)
		docID := index + 3
		if _, err := database.Exec(`INSERT INTO docs VALUES(?,0)`, key); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO _golem_fulltext_x_keys VALUES(?,?)`, docID, key); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0) VALUES(?,'alpha')`, docID); err != nil {
			t.Fatal(err)
		}
	}
	after, err := manager.Query(context.Background(), "m", "content", "alpha beta", candidates, 1)
	if err != nil || len(after) != 1 || after[0].Key != before[0].Key || after[0].Score != before[0].Score {
		t.Fatalf("hidden corpus changed ranks: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestSQLiteTermCountRankingIsExactAndSetBased(t *testing.T) {
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "fulltext-term-count.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY, allowed INTEGER NOT NULL) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,_golem_field_1,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
		`INSERT INTO docs VALUES('a',1),('b',1),('c',1),('d',0),('e',1)`,
		`INSERT INTO _golem_fulltext_x_keys VALUES(1,'a'),(2,'b'),(3,'c'),(4,'d'),(5,'e')`,
		`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0,_golem_field_1) VALUES` +
			`(1,'alpha beta gamma delta epsilon','alpha'),` +
			`(2,'alpha','beta gamma delta epsilon'),` +
			`(3,'alpha alpha','none'),` +
			`(4,'alpha beta gamma delta epsilon','alpha beta gamma delta epsilon'),` +
			`(5,'beta','alpha gamma delta epsilon')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	index := Index{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "title", Weight: 3}, {ID: "body", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{index}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs WHERE allowed=?1`, Args: []any{1}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 1000, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	ranks, err := manager.Query(context.Background(), "m", "content", `alpha beta "gamma delta" eps*`, candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"a", "b", "e", "c"}
	wantScores := []float64{13, 6, 6, 3}
	if len(ranks) != len(wantKeys) {
		t.Fatalf("ranks=%#v", ranks)
	}
	for position := range wantKeys {
		if ranks[position].Identity[0] != wantKeys[position] || ranks[position].Score != wantScores[position] {
			t.Fatalf("rank[%d]=%#v, want key=%s score=%g", position, ranks[position], wantKeys[position], wantScores[position])
		}
	}
	duplicates, err := manager.Query(context.Background(), "m", "content", `alpha alpha`, candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicates) != 4 || duplicates[0].Identity[0] != "a" || duplicates[0].Score != 4 || duplicates[1].Identity[0] != "b" || duplicates[1].Score != 3 {
		t.Fatalf("duplicate-term ranks=%#v", duplicates)
	}
	limited := candidates
	limited.MaxStatementAliases = 20
	limitedRanks, err := manager.Query(context.Background(), "m", "content", `alpha beta "gamma delta" eps*`, limited, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(limitedRanks, ranks) {
		t.Fatalf("compact fallback ranks=%#v, want %#v", limitedRanks, ranks)
	}
	parsed, err := parse(`alpha beta "gamma delta" eps*`)
	if err != nil {
		t.Fatal(err)
	}
	branches := sqliteRankBranches(index.Descriptor.Index, parsed)
	statement := manager.sqliteStatement(index, candidates, branches)
	if strings.Contains(statement, "EXISTS(SELECT") || !strings.Contains(statement, "golem_fr AS (") || !strings.Contains(statement, " UNION ALL ") {
		t.Fatalf("ranking statement is not set-based: %s", statement)
	}
	arguments := make([]any, 0, len(branches)+2)
	for _, branch := range branches {
		arguments = append(arguments, branch.expression)
	}
	arguments = append(arguments, candidates.Args...)
	arguments = append(arguments, 10)
	rows, err := database.Queryx("EXPLAIN QUERY PLAN "+statement, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToUpper(detail), "CORRELATED") {
			t.Fatalf("correlated ranking plan: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteTermCountRankingMatchesRandomizedGoOracle(t *testing.T) {
	database, _, err := sqliteprovider.New().Open(context.Background(), filepath.Join(t.TempDir(), "fulltext-oracle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY, allowed INTEGER NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,_golem_field_1,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	type document struct {
		id         string
		titleTerms map[string]bool
		bodyTerms  map[string]bool
	}
	terms := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	random := rand.New(rand.NewSource(1701))
	documents := make([]document, 64)
	tx, err := database.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for position := range documents {
		document := document{
			id:         fmt.Sprintf("d%03d", position),
			titleTerms: make(map[string]bool),
			bodyTerms:  make(map[string]bool),
		}
		for _, term := range terms {
			document.titleTerms[term] = random.Intn(3) == 0
			document.bodyTerms[term] = random.Intn(2) == 0
		}
		title := make([]string, 0, len(terms))
		body := make([]string, 0, len(terms))
		for _, term := range terms {
			if document.titleTerms[term] {
				title = append(title, term)
			}
			if document.bodyTerms[term] {
				body = append(body, term)
			}
		}
		if _, err := tx.Exec(`INSERT INTO docs VALUES(?,0,?,?)`, document.id, strings.Join(title, " "), strings.Join(body, " ")); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO _golem_fulltext_x_keys VALUES(?,?)`, position+1, document.id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0,_golem_field_1) VALUES(?,?,?)`, position+1, strings.Join(title, " "), strings.Join(body, " ")); err != nil {
			t.Fatal(err)
		}
		documents[position] = document
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "title", Weight: 3}, {ID: "body", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs WHERE allowed=?1`, Args: []any{1}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 1000, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	type expectedRank struct {
		id    string
		score float64
	}
	for trial := 0; trial < 32; trial++ {
		allowed := make(map[string]bool, len(documents))
		update, err := database.BeginTxx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, document := range documents {
			value := random.Intn(3) != 0
			allowed[document.id] = value
			if _, err := update.Exec(`UPDATE docs SET allowed=? WHERE id=?`, value, document.id); err != nil {
				t.Fatal(err)
			}
		}
		if err := update.Commit(); err != nil {
			t.Fatal(err)
		}
		queryTerms := make([]string, 1+random.Intn(4))
		for position := range queryTerms {
			queryTerms[position] = terms[random.Intn(len(terms))]
		}
		take := 1 + random.Intn(20)
		ranks, err := manager.Query(context.Background(), "m", "content", strings.Join(queryTerms, " "), candidates, take)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		uniqueTerms := make(map[string]bool, len(queryTerms))
		for _, term := range queryTerms {
			uniqueTerms[term] = true
		}
		expected := make([]expectedRank, 0, len(documents))
		for _, document := range documents {
			if !allowed[document.id] {
				continue
			}
			score := float64(0)
			for term := range uniqueTerms {
				if document.titleTerms[term] {
					score += 3
				}
				if document.bodyTerms[term] {
					score++
				}
			}
			if score > 0 {
				expected = append(expected, expectedRank{id: document.id, score: score})
			}
		}
		sort.Slice(expected, func(left, right int) bool {
			if expected[left].score != expected[right].score {
				return expected[left].score > expected[right].score
			}
			return expected[left].id < expected[right].id
		})
		if len(expected) > take {
			expected = expected[:take]
		}
		if len(ranks) != len(expected) {
			t.Fatalf("trial %d query=%q take=%d: got %d ranks, want %d", trial, strings.Join(queryTerms, " "), take, len(ranks), len(expected))
		}
		for position := range expected {
			if ranks[position].Identity[0] != expected[position].id || ranks[position].Score != expected[position].score {
				t.Fatalf("trial %d query=%q rank[%d]=%#v, want id=%s score=%g", trial, strings.Join(queryTerms, " "), position, ranks[position], expected[position].id, expected[position].score)
			}
		}
	}
}

func TestSQLiteTermCountTopTwentyKeepsAllThreeTermMatchesAheadOfWeakerRows(t *testing.T) {
	database, _, err := sqliteprovider.New().Open(context.Background(), filepath.Join(t.TempDir(), "fulltext-top-twenty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := database.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for position := 0; position < 30; position++ {
		id := fmt.Sprintf("d%02d", position)
		content := "alpha beta gamma"
		if position >= 6 {
			weaker := []string{"alpha beta", "beta gamma", "alpha gamma", "alpha", "beta", "gamma"}
			content = weaker[(position-6)%len(weaker)]
		}
		if _, err := tx.Exec(`INSERT INTO docs VALUES(?)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO _golem_fulltext_x_keys VALUES(?,?)`, position+1, id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0) VALUES(?,?)`, position+1, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "body", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs`, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 100, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	ranks, err := manager.Query(context.Background(), "m", "content", "alpha beta gamma", candidates, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 20 {
		t.Fatalf("got %d ranks, want 20", len(ranks))
	}
	for position := 0; position < 6; position++ {
		want := fmt.Sprintf("d%02d", position)
		if ranks[position].Identity[0] != want || ranks[position].Score != 3 {
			t.Fatalf("rank[%d]=%#v, want id=%s score=3", position, ranks[position], want)
		}
	}
	for position := 6; position < len(ranks); position++ {
		if ranks[position].Score >= 3 {
			t.Fatalf("weaker rank[%d]=%#v", position, ranks[position])
		}
	}
}

func TestSQLiteTermCountRankingStaysBelowCompoundSelectLimit(t *testing.T) {
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "fulltext-compound-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	columns := make([]string, 16)
	fields := make([]fulltextcontract.Field, len(columns))
	values := make([]string, len(columns))
	for index := range columns {
		columns[index] = "_golem_field_" + strconv.Itoa(index)
		fields[index] = fulltextcontract.Field{ID: "field-" + strconv.Itoa(index), Weight: 1}
		values[index] = "'t00'"
	}
	for _, statement := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(` + strings.Join(columns, ",") + `,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
		`INSERT INTO docs VALUES('a')`,
		`INSERT INTO _golem_fulltext_x_keys VALUES(1,'a')`,
		`INSERT INTO _golem_fulltext_x_fts(rowid,` + strings.Join(columns, ",") + `) VALUES(1,` + strings.Join(values, ",") + `)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: fields}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	terms := make([]string, 32)
	for index := range terms {
		terms[index] = fmt.Sprintf("t%02d", index)
	}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs`, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 999, MaxStatementBytes: 1 << 20, MaxStatementAliases: 2048, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	ranks, err := manager.Query(context.Background(), "m", "content", strings.Join(terms, " "), candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 1 || ranks[0].Identity[0] != "a" || ranks[0].Score != 16 {
		t.Fatalf("ranks=%#v", ranks)
	}
}

func TestSQLiteBM25MatchesWholeIndexRankingBeforeAuthorizationFilter(t *testing.T) {
	database, _, err := sqliteprovider.New().Open(context.Background(), filepath.Join(t.TempDir(), "fulltext-bm25.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY, allowed INTEGER NOT NULL) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
		`INSERT INTO docs VALUES('a',1),('b',1),('c',1),('hidden',0)`,
		`INSERT INTO _golem_fulltext_x_keys VALUES(1,'a'),(2,'b'),(3,'c'),(4,'hidden')`,
		`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0) VALUES(1,'alpha beta beta'),(2,'alpha'),(3,'beta'),(4,'alpha beta alpha beta alpha beta')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	index := Index{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Ranking: fulltextcontract.RankingBM25, Fields: []fulltextcontract.Field{{ID: "body", Weight: 2}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{index}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs WHERE allowed=?1`, Args: []any{1}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 1000, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	ranks, err := manager.Query(context.Background(), "m", "content", "alpha beta", candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	type expectedRank struct {
		Score float64 `db:"score"`
		ID    string  `db:"id"`
	}
	var expected []expectedRank
	if err := database.Select(&expected, `SELECT -bm25(_golem_fulltext_x_fts,2) AS score,k.id FROM _golem_fulltext_x_fts JOIN _golem_fulltext_x_keys AS k ON k.docid=_golem_fulltext_x_fts.rowid JOIN docs AS d ON d.id=k.id WHERE _golem_fulltext_x_fts MATCH ? AND d.allowed=? ORDER BY score DESC,k.id`, compileSQLite([]term{{value: "alpha"}, {value: "beta"}}, fulltextcontract.FoldingNone), 1); err != nil {
		t.Fatal(err)
	}
	if len(ranks) != len(expected) {
		t.Fatalf("ranks=%#v expected=%#v", ranks, expected)
	}
	for position := range expected {
		if ranks[position].Identity[0] != expected[position].ID || math.Abs(ranks[position].Score-expected[position].Score) > 1e-12 {
			t.Fatalf("rank[%d]=%#v expected=%#v", position, ranks[position], expected[position])
		}
	}
}

func TestSQLiteTermCountRankingScalesBelowQuadratic(t *testing.T) {
	smallManager, smallCandidates, smallClose := sqliteRankingFixture(t, 2_000)
	defer smallClose()
	largeManager, largeCandidates, largeClose := sqliteRankingFixture(t, 8_000)
	defer largeClose()
	small := fastestSQLiteRanking(t, smallManager, smallCandidates, 5)
	large := fastestSQLiteRanking(t, largeManager, largeCandidates, 5)
	if large > small*8+50*time.Millisecond {
		t.Fatalf("ranking scaled superlinearly: 2k=%s 8k=%s ratio=%.2f", small, large, float64(large)/float64(small))
	}
}

func BenchmarkSQLiteTermCountRankingScaling(b *testing.B) {
	for _, size := range []int{2_000, 8_000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			manager, candidates, closeFixture := sqliteRankingFixture(b, size)
			defer closeFixture()
			b.ReportMetric(float64(size), "rows")
			b.ResetTimer()
			for b.Loop() {
				if _, err := manager.Query(context.Background(), "m", "content", "alpha beta gamma", candidates, 20); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func sqliteRankingFixture(tb testing.TB, size int) (*Manager, semanticruntime.Candidates, func()) {
	tb.Helper()
	database, _, err := sqliteprovider.New().Open(context.Background(), filepath.Join(tb.TempDir(), fmt.Sprintf("fulltext-%d.db", size)))
	if err != nil {
		tb.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE docs(id TEXT PRIMARY KEY, allowed INTEGER NOT NULL) STRICT`,
		`CREATE TABLE _golem_fulltext_x_keys(docid INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE) STRICT`,
		`CREATE VIRTUAL TABLE _golem_fulltext_x_fts USING fts5(_golem_field_0,content='',contentless_delete=1,tokenize='unicode61 remove_diacritics 0')`,
		fmt.Sprintf(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<%d) INSERT INTO docs SELECT printf('%%08d',n),1 FROM seq`, size),
		`INSERT INTO _golem_fulltext_x_keys SELECT rowid,id FROM docs`,
		`INSERT INTO _golem_fulltext_x_fts(rowid,_golem_field_0) SELECT rowid,'alpha beta gamma' FROM docs`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			tb.Fatal(err)
		}
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "title", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs WHERE allowed=?1`, Args: []any{1}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 1000, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	return manager, candidates, func() { database.Close() }
}

func fastestSQLiteRanking(tb testing.TB, manager *Manager, candidates semanticruntime.Candidates, attempts int) time.Duration {
	tb.Helper()
	fastest := time.Duration(1<<63 - 1)
	for attempt := 0; attempt < attempts; attempt++ {
		started := time.Now()
		if _, err := manager.Query(context.Background(), "m", "content", "alpha beta gamma", candidates, 20); err != nil {
			tb.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed < fastest {
			fastest = elapsed
		}
	}
	return fastest
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
		`CREATE SCHEMA "golem_fulltext_runtime_live"`,
		`CREATE TABLE "golem_fulltext_runtime_live"."docs" ("id" text PRIMARY KEY,"allowed" boolean NOT NULL,"title" text NOT NULL)`,
		`CREATE TABLE "golem_fulltext_runtime_live"."_golem_fulltext_x_fts" ("id" text PRIMARY KEY,"document" tsvector NOT NULL)`,
		`INSERT INTO "golem_fulltext_runtime_live"."docs" VALUES ('a',true,'Renée alpha invoice.pdf Καφές 東京 Łódź'),('b',false,'alpha alpha alpha'),('c',true,'literal OR token'),('d',true,'omega psi chi'),('e',true,'omega'),('f',false,'omega psi chi omega psi chi'),('g',true,'omega psi chi')`,
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
	candidates := semanticruntime.Candidates{SQL: `SELECT "id" FROM "golem_fulltext_runtime_live"."docs" WHERE "allowed"=$1`, Args: []any{true}, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 100, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	for _, query := range []string{"alpha", "renee", `invoice.pdf`, `"invoice pdf"`, "invoice.pdf*", "inv*", "Καφές", "東京", "Łodz", "OR"} {
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
	ranks, err := manager.Query(context.Background(), "m", "content", "Lodz", candidates, 10)
	if err != nil || len(ranks) != 0 {
		t.Fatalf("non-diacritic transliteration ranks=%#v err=%v", ranks, err)
	}
	limited := candidates
	limited.MaxStatementParameters = 3
	if _, err := manager.Query(context.Background(), "m", "content", "alpha beta", limited, 10); err == nil || !strings.Contains(err.Error(), "parameter limit") {
		t.Fatalf("parameter limit error=%v", err)
	}
	defaults, err := manager.Query(context.Background(), "m", "content", "omega psi chi", candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults) != 3 || defaults[0].Identity[0] != "d" || defaults[1].Identity[0] != "g" || defaults[2].Identity[0] != "e" || defaults[0].Score != defaults[1].Score || defaults[1].Score <= defaults[2].Score {
		t.Fatalf("PostgreSQL default ranks=%#v", defaults)
	}
	manager.indexes[0].Descriptor.Index.Ranking = fulltextcontract.RankingBM25
	bm25, err := manager.Query(context.Background(), "m", "content", "omega psi chi", candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(bm25) != 3 || bm25[0].Identity[0] != "d" || bm25[1].Identity[0] != "g" || bm25[2].Identity[0] != "e" || bm25[0].Score != bm25[1].Score || bm25[1].Score <= bm25[2].Score {
		t.Fatalf("PostgreSQL BM25 ranks=%#v", bm25)
	}
}

func TestQueryParserRejectsUnsafeShapes(t *testing.T) {
	for _, test := range []struct {
		query  string
		reason string
	}{
		{"", "full-text query is empty"},
		{`"unterminated`, "full-text quoted phrase is not terminated"},
		{"x*", "full-text prefix terms require at least two characters"},
		{"a.*", "full-text prefix terms require at least two characters"},
		{"a\u0301*", "full-text prefix terms require at least two characters"},
		{string([]byte{'a', 0, 'b'}), "full-text query contains a NUL byte"},
		{string([]byte{0xff}), "full-text query is not valid UTF-8"},
		{strings.Repeat("a", 10_001), "full-text query exceeds the 10000-byte limit"},
		{" \t\n", "full-text query has no terms"},
	} {
		_, err := parse(test.query)
		if err == nil {
			t.Fatalf("query %q accepted", test.query)
		}
		if reason, ok := QueryValidationReason(err); !ok || reason != test.reason {
			t.Fatalf("query %q error=%v reason=%q typed=%t", test.query, err, reason, ok)
		}
	}
	query := ""
	for index := 0; index < 33; index++ {
		query += " word"
	}
	if _, err := parse(query); err == nil {
		t.Fatal("33 terms accepted")
	} else if reason, ok := QueryValidationReason(err); !ok || reason != "full-text query exceeds the 32-term limit" {
		t.Fatalf("33 terms error=%v reason=%q typed=%t", err, reason, ok)
	}
}

func TestDatabaseQueryFailureIsInternalAndClosed(t *testing.T) {
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "fulltext-closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{database: database, provider: ir.SQLite, indexes: []Index{{
		Descriptor: fulltextstorage.Descriptor{ModelID: "m", Storage: "_golem_fulltext_x", Index: fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "title", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}}},
	}}}
	candidates := semanticruntime.Candidates{SQL: `SELECT id FROM docs`, Columns: []string{"id"}, Model: policyir.ModelID{}, MaxStatementParameters: 100, MaxStatementBytes: 1 << 20, MaxStatementAliases: 100, NewScan: func() semanticruntime.IdentityScan { return &stringScan{} }}
	_, err = manager.Query(context.Background(), "m", "content", "alpha", candidates, 10)
	if err == nil {
		t.Fatal("closed database query succeeded")
	}
	if _, ok := QueryValidationReason(err); ok {
		t.Fatalf("database failure classified as validation: %v", err)
	}
	if got := err.Error(); got != "P9_FULLTEXT_QUERY: full-text ranking execution failed" {
		t.Fatalf("public error=%q", got)
	}
	if errors.Unwrap(err) == nil {
		t.Fatal("trusted database cause was discarded")
	}
}

func TestQueryParserRecognizesUnicodeWhitespace(t *testing.T) {
	terms, err := parse("alpha\u00a0beta\u2003\"gamma delta\"")
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != 3 || terms[0].value != "alpha" || terms[1].value != "beta" || terms[2].value != "gamma delta" || !terms[2].phrase {
		t.Fatalf("terms=%#v", terms)
	}
}

func TestQueryParserMeasuresTheFinalPrefixLexeme(t *testing.T) {
	terms, err := parse("invoice.pdf*")
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != 1 || terms[0].value != "invoice.pdf" || !terms[0].prefix {
		t.Fatalf("terms=%#v", terms)
	}
}

func TestQueryParameterReservationMatchesProviderRankingShape(t *testing.T) {
	index := Index{Descriptor: fulltextstorage.Descriptor{ModelID: "m", Index: fulltextcontract.Index{
		Name: "content", Folding: fulltextcontract.FoldingNone,
		Fields: []fulltextcontract.Field{{ID: "title", Weight: 2}, {ID: "body", Weight: 1}},
	}}}
	for _, test := range []struct {
		name     string
		provider ir.Provider
		ranking  string
		want     int
	}{
		{name: "sqlite term count", provider: ir.SQLite, ranking: fulltextcontract.RankingTermCount, want: 5},
		{name: "sqlite bm25", provider: ir.SQLite, ranking: fulltextcontract.RankingBM25, want: 2},
		{name: "postgresql term count", provider: ir.PostgreSQL, ranking: fulltextcontract.RankingTermCount, want: 3},
		{name: "postgresql bm25", provider: ir.PostgreSQL, ranking: fulltextcontract.RankingBM25, want: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := index
			selected.Descriptor.Index.Ranking = test.ranking
			manager := &Manager{provider: test.provider, indexes: []Index{selected}}
			got, err := manager.QueryParameters("m", "content", "alpha beta")
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("reserved parameters=%d want=%d", got, test.want)
			}
		})
	}
}

func TestPostgreSQLWeightsNormalizeRelativeValues(t *testing.T) {
	index := fulltextcontract.Index{Fields: []fulltextcontract.Field{{ID: "title", Weight: fulltextcontract.MaximumWeight}, {ID: "body", Weight: fulltextcontract.MaximumWeight / 2}}}
	if got, want := postgresqlWeights(index), "ARRAY[0,0,0.5,1]::real[]"; got != want {
		t.Fatalf("weights=%q, want %q", got, want)
	}
}

func TestPostgreSQLBM25UsesNormalizedCoverDensityRanking(t *testing.T) {
	index := Index{
		Descriptor: fulltextstorage.Descriptor{Storage: "search", Index: fulltextcontract.Index{Folding: fulltextcontract.FoldingNone, Fields: []fulltextcontract.Field{{ID: "body", Weight: 1}}}},
		Identity:   []physical.PhysicalColumn{{Name: "id"}},
	}
	manager := &Manager{provider: ir.PostgreSQL, schema: physical.PhysicalSchema{Namespace: physical.Namespace{Name: "app"}}}
	candidates := semanticruntime.Candidates{SQL: `SELECT "id" FROM "app"."docs"`, Columns: []string{"id"}}
	terms, err := parse("alpha beta")
	if err != nil {
		t.Fatal(err)
	}
	defaultStatement := manager.postgresqlStatement(index, terms, candidates, fulltextcontract.RankingTermCount)
	bm25Statement := manager.postgresqlStatement(index, terms, candidates, fulltextcontract.RankingBM25)
	if strings.Contains(defaultStatement, ",32)") || !strings.Contains(bm25Statement, ",32)") {
		t.Fatalf("default=%s\nbm25=%s", defaultStatement, bm25Statement)
	}
}
