package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/physical"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
)

func openQueueHistoryStore(t *testing.T, admitted bool) (*queueStore, *sqlx.DB) {
	t.Helper()
	database := sqlx.MustOpen("sqlite", "file:"+t.TempDir()+"/queue.db?_pragma=foreign_keys(1)&_txlock=immediate")
	t.Cleanup(func() { database.Close() })
	unmanaged := physical.QueueUnmanagedObjects()
	if !admitted {
		unmanaged = unmanaged[:4]
	}
	built, err := New().QueueStore(database, unmanaged)
	if err != nil {
		t.Fatal(err)
	}
	if err := built.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return built.(*queueStore), database
}

func seedQueueHistory(t *testing.T, database *sqlx.DB) {
	t.Helper()
	transaction, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := transaction.Prepare(`INSERT INTO "golem_queue" ("id","type","payload","status","attempt_count","max_attempts","available_at","enqueued_at","finished_at","updated_at") VALUES (?,?,X'00',?,0,5,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4000; index++ {
		status := "succeeded"
		switch {
		case index%97 == 0:
			status = "failed"
		case index%89 == 0:
			status = "pending"
		}
		var finished any
		if status != "pending" {
			finished = int64(index * 10)
		}
		if _, err := statement.Exec(fmt.Sprintf("%036d", index), "email", status, index*10, index*10, finished, index*10); err != nil {
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func queryPlan(t *testing.T, database *sqlx.DB, statement string, arguments []any) string {
	t.Helper()
	rows, err := database.Queryx("EXPLAIN QUERY PLAN "+statement, arguments...)
	if err != nil {
		t.Fatalf("explain %q: %v", statement, err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	return strings.Join(lines, " | ")
}

func TestQueueHistoryIndexesAreChosenWithoutAnalyze(t *testing.T) {
	store, database := openQueueHistoryStore(t, true)
	seedQueueHistory(t, database)
	cursor := time.UnixMicro(20000).UTC()
	for _, probe := range []struct {
		name      string
		statement string
		arguments []any
		want      string
		sorts     bool
	}{
		{name: "list unfiltered", want: "golem_queue_enqueued"},
		{name: "list by state", want: "golem_queue_history"},
		{name: "list failed", want: "golem_queue_terminal"},
		// Retention spans several terminal states, so SQLite merges one ordered
		// run per state. The covering index still bounds both status and
		// finished_at, which is what removes the 200k-row scan.
		{name: "retention", want: "COVERING INDEX golem_queue_terminal (status=? AND finished_at<?)", sorts: true},
	} {
		switch probe.name {
		case "list unfiltered":
			probe.statement, probe.arguments = store.listStatement(queueprovider.JobQuery{Limit: 50})
		case "list by state":
			probe.statement, probe.arguments = store.listStatement(queueprovider.JobQuery{Limit: 50, States: []queueprovider.State{queueprovider.StateSucceeded}})
		case "list failed":
			probe.statement, probe.arguments = store.listFailedStatement(queueprovider.FailedQuery{Limit: 50})
		case "retention":
			probe.statement, probe.arguments = store.retentionStatement(queueprovider.RetentionPolicy{OlderThan: cursor, MaxRows: 100})
		}
		plan := queryPlan(t, database, probe.statement, probe.arguments)
		if !strings.Contains(plan, probe.want) {
			t.Fatalf("%s does not use %s: %s\nstatement: %s", probe.name, probe.want, plan, probe.statement)
		}
		if !probe.sorts && strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("%s still sorts: %s\nstatement: %s", probe.name, plan, probe.statement)
		}
		if strings.Contains(plan, "golem_queue_claim") {
			t.Fatalf("%s fell back to the claim index: %s", probe.name, plan)
		}
		for _, step := range strings.Split(plan, " | ") {
			if strings.HasPrefix(step, "SCAN") && !strings.Contains(step, "INDEX") {
				t.Fatalf("%s scans the table without an index: %s", probe.name, plan)
			}
		}
	}
}

func TestQueueHistoryIsNotCreatedOrReliedOnWhenUnadmitted(t *testing.T) {
	store, database := openQueueHistoryStore(t, false)
	var names []string
	if err := database.Select(&names, `SELECT "name" FROM "main"."sqlite_master" WHERE "type"='index' AND "tbl_name"='golem_queue' AND "sql" IS NOT NULL ORDER BY "name"`); err != nil {
		t.Fatal(err)
	}
	want := []string{"golem_queue_claim", "golem_queue_dedupe", "golem_queue_exclusive"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("unadmitted store created %v, want %v", names, want)
	}
	for _, statement := range []string{
		mustStatement(store.listStatement(queueprovider.JobQuery{Limit: 50})),
		mustStatement(store.listStatement(queueprovider.JobQuery{Limit: 50, States: []queueprovider.State{queueprovider.StateSucceeded}})),
		mustStatement(store.listFailedStatement(queueprovider.FailedQuery{Limit: 50})),
		mustStatement(store.retentionStatement(queueprovider.RetentionPolicy{OlderThan: time.UnixMicro(1000).UTC(), MaxRows: 10})),
	} {
		if strings.Contains(statement, "INDEXED BY") {
			t.Fatalf("unadmitted store named an index it never created: %s", statement)
		}
	}
}

func mustStatement(statement string, _ []any) string { return statement }

func TestQueueIndexVerificationRefusesForeignAndRedefinedIndexes(t *testing.T) {
	store, database := openQueueHistoryStore(t, true)
	ctx := context.Background()
	if _, err := database.Exec(`CREATE INDEX "golem_queue_sneaky" ON "golem_queue" ("type")`); err != nil {
		t.Fatal(err)
	}
	err := store.verifyQueueGuarantees(ctx)
	if err == nil || !strings.Contains(err.Error(), "was not created by golem") {
		t.Fatalf("a foreign index on golem_queue was tolerated: %v", err)
	}
	if _, err := database.Exec(`DROP INDEX "golem_queue_sneaky"`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP INDEX "golem_queue_terminal"`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE INDEX "golem_queue_terminal" ON "golem_queue" ("finished_at")`); err != nil {
		t.Fatal(err)
	}
	err = store.verifyQueueGuarantees(ctx)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a redefined golem index was tolerated: %v", err)
	}
}

func TestQueueHistoryDoesNotChangeOperatorResults(t *testing.T) {
	ctx := context.Background()
	admitted, admittedDatabase := openQueueHistoryStore(t, true)
	plain, plainDatabase := openQueueHistoryStore(t, false)
	seedQueueHistory(t, admittedDatabase)
	seedQueueHistory(t, plainDatabase)

	for _, query := range []queueprovider.JobQuery{
		{Limit: 25},
		{Limit: 25, States: []queueprovider.State{queueprovider.StateSucceeded}},
		{Limit: 25, States: []queueprovider.State{queueprovider.StateFailed, queueprovider.StatePending}},
		{Limit: 25, Types: []string{"email"}},
	} {
		left, err := admitted.List(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		right, err := plain.List(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(left.Jobs) != len(right.Jobs) || left.More != right.More {
			t.Fatalf("list results diverge for %+v: %d/%v vs %d/%v", query, len(left.Jobs), left.More, len(right.Jobs), right.More)
		}
		for index := range left.Jobs {
			if left.Jobs[index].ID != right.Jobs[index].ID {
				t.Fatalf("list order diverges for %+v at %d: %s vs %s", query, index, left.Jobs[index].ID, right.Jobs[index].ID)
			}
		}
	}

	leftFailed, err := admitted.ListFailed(ctx, queueprovider.FailedQuery{Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	rightFailed, err := plain.ListFailed(ctx, queueprovider.FailedQuery{Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(leftFailed.Jobs) != len(rightFailed.Jobs) {
		t.Fatalf("failed listing diverges: %d vs %d", len(leftFailed.Jobs), len(rightFailed.Jobs))
	}
	for index := range leftFailed.Jobs {
		if leftFailed.Jobs[index].ID != rightFailed.Jobs[index].ID {
			t.Fatalf("failed listing order diverges at %d", index)
		}
	}

	policy := queueprovider.RetentionPolicy{OlderThan: time.UnixMicro(200000).UTC(), MaxRows: 500}
	leftDeleted, err := admitted.RunRetention(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	rightDeleted, err := plain.RunRetention(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if leftDeleted != rightDeleted || leftDeleted == 0 {
		t.Fatalf("retention diverges: %d vs %d", leftDeleted, rightDeleted)
	}
}

func TestAdmittedQueueObjectsNeverDriftWhetherOrNotAWorkerRan(t *testing.T) {
	ctx := context.Background()
	provider := New()
	schema, err := provider.Lower(ctx, socialModelIR(), physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []struct {
		name    string
		prepare func(*testing.T, *sqlx.DB)
	}{
		{name: "no worker ever started", prepare: func(*testing.T, *sqlx.DB) {}},
		{name: "worker created every admitted object", prepare: func(t *testing.T, database *sqlx.DB) {
			store, storeErr := provider.QueueStore(database, schema.Unmanaged)
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			if err := store.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "worker predates the history indexes", prepare: func(t *testing.T, database *sqlx.DB) {
			store, storeErr := provider.QueueStore(database, nil)
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			if err := store.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(stage.name, func(t *testing.T) {
			database, _, openErr := provider.Open(ctx, filepath.Join(t.TempDir(), "drift.db"))
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer database.Close()
			if err := provider.ApplyInitial(ctx, database, schema); err != nil {
				t.Fatal(err)
			}
			stage.prepare(t, database)
			actual, introspectErr := provider.Introspect(ctx, database, schema)
			if introspectErr != nil {
				t.Fatalf("admitted queue storage was reported as drift: %v", introspectErr)
			}
			want, _ := physical.PhysicalFingerprint(schema)
			got, _ := physical.PhysicalFingerprint(actual)
			if got != want {
				t.Fatalf("fingerprint=%s want %s", got, want)
			}
		})
	}
}

func TestAnUnknownQueueIndexIsAlwaysDrift(t *testing.T) {
	ctx := context.Background()
	provider := New()
	schema, err := provider.Lower(ctx, socialModelIR(), physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	database, _, openErr := provider.Open(ctx, filepath.Join(t.TempDir(), "drift.db"))
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer database.Close()
	if err := provider.ApplyInitial(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE "golem_queue" ("id" TEXT PRIMARY KEY NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE INDEX "golem_queue_invented" ON "golem_queue" ("id")`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Introspect(ctx, database, schema); err == nil {
		t.Fatal("an index golem never created was admitted as queue storage")
	}
}

// releasedQueueSchemaV030 is the verbatim durable-queue DDL shipped by
// go/v0.3.0 through go/v0.4.0, copied from the module cache. An upgrading
// database must satisfy the new index verification without being touched.
var releasedQueueSchemaV030 = []string{
	`CREATE TABLE IF NOT EXISTS "main"."golem_queue" ("id" TEXT PRIMARY KEY NOT NULL,"type" TEXT NOT NULL,"payload" BLOB NOT NULL,"status" TEXT NOT NULL,"attempt_count" INTEGER NOT NULL DEFAULT 0,"max_attempts" INTEGER NOT NULL,"available_at" INTEGER NOT NULL,"lease_token" TEXT,"lease_until" INTEGER,"resource_name" TEXT,"resource_cost" INTEGER,"resource_capacity" INTEGER,"dedupe_key" TEXT,"exclusive_key" TEXT,"cancel_requested_at" INTEGER,"last_code" TEXT,"enqueued_at" INTEGER NOT NULL,"finished_at" INTEGER,"updated_at" INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS "main"."golem_queue_claim" ON "golem_queue" ("status","available_at","type")`,
	`CREATE UNIQUE INDEX IF NOT EXISTS "main"."golem_queue_dedupe" ON "golem_queue" ("dedupe_key") WHERE "status" IN ('pending','leased')`,
	`CREATE INDEX IF NOT EXISTS "main"."golem_queue_exclusive" ON "golem_queue" ("exclusive_key") WHERE "status"='leased'`,
}

func openReleasedQueueDatabase(t *testing.T) *sqlx.DB {
	t.Helper()
	database := sqlx.MustOpen("sqlite", "file:"+t.TempDir()+"/released.db?_pragma=foreign_keys(1)&_txlock=immediate")
	t.Cleanup(func() { database.Close() })
	for _, statement := range releasedQueueSchemaV030 {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("create released queue storage %q: %v", statement, err)
		}
	}
	return database
}

func TestAQueueDatabaseFromV030ThroughV040PassesIndexVerification(t *testing.T) {
	ctx := context.Background()
	for _, admitted := range []bool{false, true} {
		database := openReleasedQueueDatabase(t)
		unmanaged := physical.QueueUnmanagedObjects()
		if !admitted {
			unmanaged = unmanaged[:4]
		}
		store, err := New().QueueStore(database, unmanaged)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.EnsureSchema(ctx); err != nil {
			t.Fatalf("admitted=%v: a released queue database was refused on upgrade: %v", admitted, err)
		}
		if err := store.(*queueStore).verifyQueueGuarantees(ctx); err != nil {
			t.Fatalf("admitted=%v: released queue indexes failed verification: %v", admitted, err)
		}
	}
}

func TestAUserIndexOnTheQueueTableIsRefusedByName(t *testing.T) {
	ctx := context.Background()
	database := openReleasedQueueDatabase(t)
	if _, err := database.Exec(`CREATE INDEX "tenant_queue_by_type" ON "golem_queue" ("type")`); err != nil {
		t.Fatal(err)
	}
	store, err := New().QueueStore(database, physical.QueueUnmanagedObjects())
	if err != nil {
		t.Fatal(err)
	}
	err = store.EnsureSchema(ctx)
	if err == nil {
		t.Fatal("a user-authored index on golem_queue was tolerated")
	}
	for _, fragment := range []string{"tenant_queue_by_type", "drop it"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("refusal must name the object and say what to do: %v", err)
		}
	}
}

// TestEveryNamedIndexExistsWheneverTheStoreNamesOne pins the invariant the two
// query shapes rest on: if the store names an index, EnsureSchema created it,
// so a prepared statement can never fail with "no such index".
func TestEveryNamedIndexExistsWheneverTheStoreNamesOne(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		store, database := openQueueHistoryStore(t, admitted)
		present := map[string]bool{}
		var names []string
		if err := database.Select(&names, `SELECT "name" FROM "main"."sqlite_master" WHERE "type"='index' AND "tbl_name"='golem_queue'`); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			present[name] = true
		}
		type prepared struct {
			statement string
			arguments []any
		}
		statements := make([]prepared, 0, 5)
		add := func(statement string, arguments []any) {
			statements = append(statements, prepared{statement: statement, arguments: arguments})
		}
		add(store.listStatement(queueprovider.JobQuery{Limit: 10}))
		add(store.listStatement(queueprovider.JobQuery{Limit: 10, States: []queueprovider.State{queueprovider.StateSucceeded}}))
		add(store.listStatement(queueprovider.JobQuery{Limit: 10, Types: []string{"email"}}))
		add(store.listFailedStatement(queueprovider.FailedQuery{Limit: 10}))
		add(store.retentionStatement(queueprovider.RetentionPolicy{OlderThan: time.UnixMicro(1000).UTC(), MaxRows: 10}))
		named := 0
		for _, built := range statements {
			statement := built.statement
			for _, fragment := range strings.Split(statement, `INDEXED BY "`)[1:] {
				name := fragment[:strings.Index(fragment, `"`)]
				named++
				if !present[name] {
					t.Fatalf("admitted=%v: statement names %s, which EnsureSchema never created: %s", admitted, name, statement)
				}
			}
			if _, err := database.Exec("EXPLAIN "+statement, built.arguments...); err != nil {
				t.Fatalf("admitted=%v: statement does not prepare: %v\n%s", admitted, err, statement)
			}
		}
		if admitted && named == 0 {
			t.Fatal("an admitted store named no index at all")
		}
		if !admitted && named != 0 {
			t.Fatalf("an unadmitted store named %d indexes", named)
		}
	}
}

// TestSQLiteRefusesAMissingQueueIndex covers the same database-scoped-name hole
// PostgreSQL has: SQLite index names are unique per database, so an index of
// that name on another table makes CREATE INDEX IF NOT EXISTS skip silently.
func TestSQLiteRefusesAMissingQueueIndex(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"golem_queue_claim", "golem_queue_exclusive", "golem_queue_enqueued", "golem_queue_history", "golem_queue_terminal"} {
		t.Run(name, func(t *testing.T) {
			store, database := openQueueHistoryStore(t, true)
			if _, err := database.Exec(`DROP INDEX "` + name + `"`); err != nil {
				t.Fatal(err)
			}
			err := store.verifyQueueGuarantees(ctx)
			if err == nil {
				t.Fatalf("a missing %s was accepted", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("refusal must name the absent object, got: %v", err)
			}
		})
	}
}

func TestSQLiteNameCollisionOnAnotherTableIsCaught(t *testing.T) {
	ctx := context.Background()
	database := sqlx.MustOpen("sqlite", "file:"+t.TempDir()+"/collision.db?_pragma=foreign_keys(1)&_txlock=immediate")
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(`CREATE TABLE "decoy" ("status" TEXT, "available_at" INTEGER, "type" TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE INDEX "golem_queue_claim" ON "decoy" ("status")`); err != nil {
		t.Fatal(err)
	}
	store, err := New().QueueStore(database, physical.QueueUnmanagedObjects())
	if err != nil {
		t.Fatal(err)
	}
	err = store.EnsureSchema(ctx)
	if err == nil {
		t.Fatal("a queue whose claim index name was taken by another table started anyway")
	}
	if !strings.Contains(err.Error(), "golem_queue_claim") {
		t.Fatalf("refusal must name the absent index, got: %v", err)
	}
}
