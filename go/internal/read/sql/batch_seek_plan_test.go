package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

type batchSeekFixture struct {
	fixture schematest.Fixture
	keys    [][]policyir.Value
}

func newBatchSeekFixture(tb testing.TB, parents int) batchSeekFixture {
	tb.Helper()
	fixture := schematest.New(tb)
	keys := make([][]policyir.Value, parents)
	for index := range keys {
		identity, err := golem.ParseUUID(fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1))
		if err != nil {
			tb.Fatal(err)
		}
		keys[index] = []policyir.Value{policyir.UUIDValue(identity.Bytes())}
	}
	return batchSeekFixture{fixture: fixture, keys: keys}
}

func (seek batchSeekFixture) render(tb testing.TB, provider policyir.Provider, take int) BatchStatement {
	tb.Helper()
	fixture := seek.fixture
	userDescriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	title := golem.GeneratedTextField[renderPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[renderUser, renderPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	frozen, err := golem.FreezeFindMany(userDescriptor, golem.Select[renderUser](posts.Args(golem.OrderBy(title.Asc()), golem.Take[renderPost](take), golem.Select[renderPost](title))))
	if err != nil {
		tb.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		tb.Fatal(err)
	}
	planned, err := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
	if err != nil {
		tb.Fatal(err)
	}
	endpoint, ok := fixture.Registry.RelationEndpoint(fixture.User, fixture.UserPosts, fixture.Authorship)
	if !ok {
		tb.Fatal("relation endpoint is absent")
	}
	proof, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		tb.Fatal(err)
	}
	statement, err := RenderBatch(planned.Relations()[0].Child(), endpoint, seek.keys, fixture.Registry, provider, proof)
	if err != nil {
		tb.Fatal(err)
	}
	return statement
}

func seedBatchSeekChildren(tb testing.TB, database *sqlx.DB, provider policyir.Provider, parents, children int) {
	tb.Helper()
	statements := []string{
		`INSERT INTO "users" ("id","name") SELECT printf('00000000-0000-4000-8000-%012d', value), 'user' FROM generate_series(1, ?1)`,
		`INSERT INTO "posts" ("id","author_id","title") SELECT printf('%08x-0000-4000-8000-000000000000', value), printf('00000000-0000-4000-8000-%012d', 1 + value % ?1), printf('title-%09d', value) FROM generate_series(1, ?2)`,
		`CREATE INDEX "posts_author_title" ON "posts" ("author_id", "title", "id")`,
		`ANALYZE`,
	}
	if provider == policyir.ProviderPostgreSQL {
		statements = []string{
			`INSERT INTO "users" ("id","name") SELECT ('00000000-0000-4000-8000-' || lpad(value::text, 12, '0'))::uuid, 'user' FROM generate_series(1, $1) AS value`,
			`INSERT INTO "posts" ("id","author_id","title") SELECT (lpad(to_hex(value), 8, '0') || '-0000-4000-8000-000000000000')::uuid, ('00000000-0000-4000-8000-' || lpad((1 + value % $1)::text, 12, '0'))::uuid, 'title-' || lpad(value::text, 9, '0') FROM generate_series(1, $2) AS value`,
			`CREATE INDEX "posts_author_title" ON "posts" ("author_id", "title" COLLATE "C", "id")`,
			`ANALYZE`,
		}
	}
	for index, statement := range statements {
		var arguments []any
		switch index {
		case 0:
			arguments = []any{parents}
		case 1:
			arguments = []any{parents, parents * children}
		}
		if _, err := database.Exec(statement, arguments...); err != nil {
			tb.Fatalf("seed %q: %v", statement, err)
		}
	}
}

func openBatchSeekSQLite(tb testing.TB, fixture schematest.Fixture) *sqlx.DB {
	tb.Helper()
	ctx := context.Background()
	database, _, err := sqlite.New().Open(ctx, filepath.Join(tb.TempDir(), "batch-seek.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		tb.Fatal(err)
	}
	return database
}

func openBatchSeekPostgreSQL(tb testing.TB, fixture schematest.Fixture) *sqlx.DB {
	tb.Helper()
	database, err := sqlx.Open("pgx", testenv.DisposablePostgreSQL(tb, testenv.PostgreSQLDSNVariable))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { database.Close() })
	if err := postgresql.New().ApplyInitial(context.Background(), database, fixture.PostgreSQL); err != nil {
		tb.Fatal(err)
	}
	return database
}

type batchSeekPlanStep struct {
	id, parent int
	detail     string
}

func TestSQLiteBatchRelationSeeksEachParentInOrder(t *testing.T) {
	seek := newBatchSeekFixture(t, 4)
	database := openBatchSeekSQLite(t, seek.fixture)
	seedBatchSeekChildren(t, database, policyir.ProviderSQLite, 4, 500)
	statement := seek.render(t, policyir.ProviderSQLite, 3)
	rows, err := database.Queryx("EXPLAIN QUERY PLAN "+statement.SQL(), statement.Args()...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, statement.SQL())
	}
	var steps []batchSeekPlanStep
	for rows.Next() {
		var step batchSeekPlanStep
		var unused int
		if err := rows.Scan(&step.id, &step.parent, &unused, &step.detail); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, step)
	}
	rows.Close()
	lines := make([]string, len(steps))
	seeks := 0
	for index, step := range steps {
		lines[index] = fmt.Sprintf("%d<-%d %s", step.id, step.parent, step.detail)
		if strings.HasPrefix(step.detail, "SEARCH golem_br0 USING ") && strings.Contains(step.detail, "INDEX posts_author_title (author_id=?)") {
			seeks++
		}
	}
	plan := strings.Join(lines, "\n")
	for _, sort := range steps {
		if !strings.HasPrefix(sort.detail, "USE TEMP B-TREE FOR") {
			continue
		}
		for _, sibling := range steps {
			if sibling.parent == sort.parent && ((strings.HasPrefix(sibling.detail, "SEARCH golem_br0 ") && !strings.HasSuffix(sibling.detail, "(id=?)")) || strings.HasPrefix(sibling.detail, "SCAN golem_br0")) {
				t.Fatalf("the batch sorts every child of the chunk:\n%s\n%s", plan, statement.SQL())
			}
		}
	}
	if seeks == 0 {
		t.Fatalf("the batch does not seek children by parent in title order:\n%s\n%s", plan, statement.SQL())
	}
}

func TestPostgreSQLBatchRelationReadsOnlyEachParentsPage(t *testing.T) {
	seek := newBatchSeekFixture(t, 4)
	database := openBatchSeekPostgreSQL(t, seek.fixture)
	seedBatchSeekChildren(t, database, policyir.ProviderPostgreSQL, 4, 2000)
	statement := seek.render(t, policyir.ProviderPostgreSQL, 3)
	var raw string
	if err := database.Get(&raw, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, FORMAT JSON) "+statement.SQL(), statement.Args()...); err != nil {
		t.Fatalf("explain: %v\n%s", err, statement.SQL())
	}
	var plans []struct {
		Plan batchSeekPGNode `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(raw), &plans); err != nil || len(plans) != 1 {
		t.Fatalf("plan %s: %v", raw, err)
	}
	read := 0.0
	plans[0].Plan.walk(func(node batchSeekPGNode) {
		if node.RelationName == "posts" {
			read += node.ActualRows * node.ActualLoops
		}
	})
	if read == 0 || read > 4*3*2 {
		t.Fatalf("the batch read %v child rows to return 3 per parent for 4 parents:\n%s\n%s", read, raw, statement.SQL())
	}
}

type batchSeekPGNode struct {
	RelationName string            `json:"Relation Name"`
	ActualRows   float64           `json:"Actual Rows"`
	ActualLoops  float64           `json:"Actual Loops"`
	Plans        []batchSeekPGNode `json:"Plans"`
}

func (node batchSeekPGNode) walk(visit func(batchSeekPGNode)) {
	visit(node)
	for _, child := range node.Plans {
		child.walk(visit)
	}
}

func TestBatchRelationSeekMatchesTheRankedPage(t *testing.T) {
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		seek := newBatchSeekFixture(t, 4)
		var database *sqlx.DB
		if provider == policyir.ProviderSQLite {
			database = openBatchSeekSQLite(t, seek.fixture)
		} else {
			database = openBatchSeekPostgreSQL(t, seek.fixture)
		}
		seedBatchSeekChildren(t, database, provider, 4, 50)
		for _, take := range []int{3, -3} {
			statement := seek.render(t, provider, take)
			rows, err := database.Queryx(statement.SQL(), statement.Args()...)
			if err != nil {
				t.Fatalf("provider %d take %d: %v\n%s", provider, take, err, statement.SQL())
			}
			var got []string
			for rows.Next() {
				values, err := rows.SliceScan()
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, fmt.Sprintf("%v|%x", values[0], values[len(values)-1]))
			}
			rows.Close()
			want := make([]string, 0, 12)
			for parent := 1; parent <= 4; parent++ {
				var titles []int
				for value := 1; value <= 200; value++ {
					if 1+value%4 == parent {
						titles = append(titles, value)
					}
				}
				if take < 0 {
					titles = titles[len(titles)-3:]
					titles = []int{titles[2], titles[1], titles[0]}
				} else {
					titles = titles[:3]
				}
				for _, value := range titles {
					identity, _ := golem.ParseUUID(fmt.Sprintf("00000000-0000-4000-8000-%012d", parent))
					want = append(want, fmt.Sprintf("title-%09d|%x", value, identity.Bytes()))
				}
			}
			if len(got) != len(want) {
				t.Fatalf("provider %d take %d rows=%v want %v", provider, take, got, want)
			}
			for index := range want {
				if !strings.HasPrefix(got[index], strings.Split(want[index], "|")[0]) {
					t.Fatalf("provider %d take %d row %d=%s want %s\nall=%v", provider, take, index, got[index], want[index], got)
				}
			}
		}
	}
}

func BenchmarkBatchRelationLoad(b *testing.B) {
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		for _, children := range []int{200, 20000} {
			b.Run(fmt.Sprintf("provider=%d/children=%d", provider, children), func(b *testing.B) {
				seek := newBatchSeekFixture(b, 5)
				var database *sqlx.DB
				if provider == policyir.ProviderSQLite {
					database = openBatchSeekSQLite(b, seek.fixture)
				} else {
					database = openBatchSeekPostgreSQL(b, seek.fixture)
				}
				seedBatchSeekChildren(b, database, provider, 5, children)
				statement := seek.render(b, provider, 10)
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					rows, err := database.Queryx(statement.SQL(), statement.Args()...)
					if err != nil {
						b.Fatal(err)
					}
					count := 0
					for rows.Next() {
						count++
					}
					rows.Close()
					if count != 50 {
						b.Fatalf("loaded %d children", count)
					}
				}
			})
		}
	}
}
