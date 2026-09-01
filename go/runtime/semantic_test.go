package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/eleven-am/golem/go/queue"
	"github.com/jmoiron/sqlx"
)

func TestSemanticRanksRemovedBeforeHydrationAreOmitted(t *testing.T) {
	ranks := []semanticruntime.Rank{{Key: "foreign", Distance: 0}, {Key: "other-foreign", Distance: 1}}
	result, err := assembleSemanticResults(ranks, map[string]semanticHydratedRow[struct{}]{})
	if err != nil || len(result) != 0 {
		t.Fatalf("removed ranks result=%#v error=%v", result, err)
	}
}

func TestSemanticQueryEncodingFailsBeforeReadPlanning(t *testing.T) {
	queries := []string{
		string([]byte{0xff}),
		strings.Repeat("x", embedding.MaximumInputBytes+1),
	}
	for _, query := range queries {
		if _, err := semanticReadOptions[struct{}](nil, query, 1); err == nil {
			t.Fatal("invalid semantic query was accepted")
		} else if code, ok := embedding.CodeOf(err); !ok || code != embedding.CodeInvalidInput {
			t.Fatalf("invalid query error=%v code=%q ok=%t", err, code, ok)
		}
	}
}

type semanticLimitUser struct{}

func TestSemanticResultLimitUsesReadCaps(t *testing.T) {
	for _, test := range []struct {
		name      string
		modelTake uint32
		appTake   int
	}{
		{name: "model", modelTake: 5},
		{name: "application", appTake: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := schematest.NewWithMaxTake(t, test.modelTake, 0)
			descriptor := golem.GeneratedModelDescriptor[semanticLimitUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
			name := golem.GeneratedTextField[semanticLimitUser, string](fixture.UserName)
			options, err := semanticCandidateOptions[semanticLimitUser](nil, 6)
			if err != nil {
				t.Fatal(err)
			}
			options = append(options, golem.Select[semanticLimitUser](name))
			frozen, err := golem.FreezeFindMany(descriptor, options...)
			if err != nil {
				t.Fatal(err)
			}
			request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
			if err != nil {
				t.Fatal(err)
			}
			limits := readplan.DefaultLimits()
			limits.MaxTake = test.appTake
			if _, err := readplan.System(request, fixture.Registry, limits); err == nil {
				t.Fatal("semantic result limit bypassed the read cap")
			}
		})
	}
}

func TestSemanticIdentityChunkAccountsForExistingBinds(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[semanticLimitUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[semanticLimitUser, string](fixture.UserName)
	options, err := semanticCandidateOptions[semanticLimitUser](nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	options = append(options, golem.Select[semanticLimitUser](name))
	frozen, err := golem.FreezeFindMany(descriptor, options...)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	limits := readplan.DefaultLimits()
	limits.MaxStatementParameters = 999
	planned, err := readplan.System(request, fixture.Registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	if got := semanticIdentityChunkSize(planned, 1, 951); got != 48 {
		t.Fatalf("identity chunk=%d want=48", got)
	}
	if err := validateSemanticPlanTake(PreparedRead{request: request}, planned, "similar", 101); err == nil {
		t.Fatal("similar limit overflow was accepted")
	} else {
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Operation != "similar" {
			t.Fatalf("similar limit error=%v", err)
		}
	}
}

func TestSemanticHydrationChunkShrinksOnlyForByteCeiling(t *testing.T) {
	model := policyir.ModelID{1}
	overflow := readsql.ValidateStatementComplexity(model, strings.Repeat("x", 21), 20, readsql.MaxStatementAliases)
	if end, retry := reduceSemanticHydrationChunk(4, 12, overflow); !retry || end != 8 {
		t.Fatalf("byte overflow reduced to %d retry=%t", end, retry)
	}
	if end, retry := reduceSemanticHydrationChunk(4, 5, overflow); retry || end != 5 {
		t.Fatalf("single identity reduced to %d retry=%t", end, retry)
	}
	if end, retry := reduceSemanticHydrationChunk(4, 12, errors.New("render failed")); retry || end != 12 {
		t.Fatalf("unrelated error reduced to %d retry=%t", end, retry)
	}
}

func TestSemanticHydrationKeysPrivateIdentityCells(t *testing.T) {
	fixture := schematest.New(t)
	field := policyir.FieldID(fixture.UserID)
	decoder, err := readdecode.NewFields(policyir.ModelID(fixture.User), fixture.Registry, policyir.ProviderSQLite, []policyir.FieldID{field})
	if err != nil {
		t.Fatal(err)
	}
	cells, err := decoder.Values([]any{"10000000-0000-0000-0000-000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 1 || cells[0].Public() {
		t.Fatalf("identity cells=%d public=%t", len(cells), len(cells) == 1 && cells[0].Public())
	}
	key, err := semanticHydrationRecordKey(map[policyir.FieldID]readdecode.Cell{field: cells[0]}, []policyir.FieldID{field})
	if err != nil {
		t.Fatal(err)
	}
	uuid, err := golem.ParseUUID("10000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	want, err := semantickey.Encode([]any{uuid})
	if err != nil {
		t.Fatal(err)
	}
	if key != want {
		t.Fatalf("private identity key=%q want=%q", key, want)
	}
}

func TestSemanticRecordKeyValueCoversMutationIdentityKinds(t *testing.T) {
	signed, err := policyir.SignedValue(policyir.ValueInt64, 42)
	if err != nil {
		t.Fatal(err)
	}
	text, err := policyir.StringValue("identity")
	if err != nil {
		t.Fatal(err)
	}
	instant, err := policyir.NewDateTimeValue(1_700_000_000, 123_456_000)
	if err != nil {
		t.Fatal(err)
	}
	uuid := [16]byte{15: 1}
	for _, test := range []struct {
		name  string
		value policyir.Value
		want  any
	}{
		{name: "bool", value: policyir.BoolValue(true), want: true},
		{name: "integer", value: signed, want: int64(42)},
		{name: "string", value: text, want: "identity"},
		{name: "bytes", value: policyir.BytesValue([]byte{1, 2, 3}), want: []byte{1, 2, 3}},
		{name: "uuid", value: policyir.UUIDValue(uuid), want: uuid},
		{name: "datetime", value: instant, want: time.Unix(1_700_000_000, 123_456_000).UTC()},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := semanticRecordKeyValue(test.value)
			if !ok {
				t.Fatal("identity kind was rejected")
			}
			gotKey, gotErr := semantickey.Encode([]any{got})
			wantKey, wantErr := semantickey.Encode([]any{test.want})
			if gotErr != nil || wantErr != nil || gotKey != wantKey {
				t.Fatalf("key=%q want=%q errors=%v/%v", gotKey, wantKey, gotErr, wantErr)
			}
		})
	}
}

const semanticJobStateTable = "_golem_semantic_semantic-post-related_state"
const semanticJobVectorTable = "_golem_semantic_semantic-post-related_vec"

type semanticJobFixture struct {
	database *sqlx.DB
	app      *App[testPrincipal, testActor]
	embedder *semanticJobEmbedder
}

type semanticJobEmbedder struct {
	specification embedding.Specification
	database      *sqlx.DB
	beforeEmbed   func(*sqlx.DB)
	calls         int
}

func (provider *semanticJobEmbedder) Specification() embedding.Specification {
	return provider.specification
}

func (provider *semanticJobEmbedder) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	if provider.beforeEmbed != nil {
		provider.beforeEmbed(provider.database)
	}
	provider.calls += len(inputs)
	result := make([]embedding.Vector, len(inputs))
	for index, input := range inputs {
		values := []float32{0, 0, 1}
		if strings.Contains(input.Text(), "alpha") {
			values = []float32{1, 0, 0}
		}
		result[index], _ = embedding.NewVector(values)
	}
	return result, nil
}

var semanticJobModel = golem.ModelID{0x91}

func semanticJobModelID() ir.ModelID { return ir.ModelID(hex.EncodeToString(semanticJobModel[:])) }

func semanticJobPhysicalSchema(t *testing.T) physical.PhysicalSchema {
	t.Helper()
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{"title"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	owner := physical.PhysicalTable{
		ID: semanticJobModelID(), Name: "posts",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "post-primary", Name: "pk_posts", Columns: []ir.FieldID{"id"}},
	}
	extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: "semantic-post-related", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: 1, Owner: ir.ObjectID(semanticJobModelID()), Payload: payload}, owner)
	if err != nil {
		t.Fatal(err)
	}
	return physical.PhysicalSchema{Namespace: physical.Namespace{Name: "main"}, Tables: []physical.PhysicalTable{owner}, Extensions: []physical.Extension{extension}}
}

func newSemanticJobFixture(t *testing.T, hook func(*sqlx.DB)) semanticJobFixture {
	t.Helper()
	ctx := context.Background()
	handle, err := sqlitevec.Open("file:" + filepath.Join(t.TempDir(), "semantic-jobs.db") + "?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	database := sqlx.NewDb(handle, "sqlite3")
	if _, err := database.Exec(`
CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT);
CREATE TABLE "` + semanticJobStateTable + `" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL) STRICT;
CREATE INDEX "_golem_semantic_semantic-post-related_state_stale" ON "` + semanticJobStateTable + `" ("record_key" ASC) WHERE "status" <> 'ready';
CREATE VIRTUAL TABLE "` + semanticJobVectorTable + `" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine);
INSERT INTO "posts" (id,title) VALUES ('a','alpha'),('b','beta')`); err != nil {
		t.Fatal(err)
	}
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 8)
	embedder := &semanticJobEmbedder{specification: specification, database: database, beforeEmbed: hook}
	registry, err := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	if err != nil {
		t.Fatal(err)
	}
	schema := semanticJobPhysicalSchema(t)
	inventory, err := semanticruntime.NewInventory(schema, registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := semanticruntime.NewManager(database, ir.SQLite, schema, inventory)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqliteprovider.New().QueueStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	app := &App[testPrincipal, testActor]{database: database, semantic: manager, queueStore: store, queueLimits: queue.DefaultLimits().Resolved()}
	if err := app.registerSemanticJobs(queue.NewRegistry()); err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	return semanticJobFixture{database: database, app: app, embedder: embedder}
}

func (fixture semanticJobFixture) jobs(t *testing.T, name string) int {
	t.Helper()
	var count int
	if err := fixture.database.Get(&count, `SELECT COUNT(*) FROM "golem_queue" WHERE "type"=?`, name); err != nil {
		t.Fatal(err)
	}
	return count
}

func (fixture semanticJobFixture) mark(t *testing.T, id string) {
	t.Helper()
	key, err := semantickey.Encode([]any{id})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.database.Exec(`UPDATE "`+semanticJobStateTable+`" SET status='pending' WHERE record_key=?`, key)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("mark %q affected=%d", id, affected)
	}
}

// TestSemanticStartupRefusesAnUnqueuedIndex covers the whole refusal: Golem
// keeps the index current itself, and it cannot do that with nowhere to record
// the work. There is no acknowledgement that buys an application out of it.
func TestSemanticStartupRefusesAnUnqueuedIndex(t *testing.T) {
	ctx := context.Background()
	fixture := newSemanticJobFixture(t, nil)
	unqueued := &App[testPrincipal, testActor]{database: fixture.database, semantic: fixture.app.semantic}
	err := unqueued.startSemanticJobs(ctx)
	if err == nil || !strings.Contains(err.Error(), "durable job queue") {
		t.Fatalf("unqueued semantic index error=%v", err)
	}
	if fixture.jobs(t, semanticReconcileJobType) != 0 {
		t.Fatal("a refused startup still enqueued work")
	}
	empty := &App[testPrincipal, testActor]{}
	if err := empty.startSemanticJobs(ctx); err != nil {
		t.Fatalf("application without a semantic index was refused: %v", err)
	}
}

// TestSemanticStartupEnqueuesOneDedupedReconcilePerIndex bounds how long a
// write Golem never saw can stay invisible: one deployment. Two instances
// starting against one database must still leave one job.
func TestSemanticStartupEnqueuesOneDedupedReconcilePerIndex(t *testing.T) {
	ctx := context.Background()
	fixture := newSemanticJobFixture(t, nil)
	fixture.app.semanticReconcileInterval = time.Minute
	if err := fixture.app.startSemanticJobs(ctx); err != nil {
		t.Fatal(err)
	}
	second := &App[testPrincipal, testActor]{database: fixture.database, semantic: fixture.app.semantic, queueStore: fixture.app.queueStore, queueLimits: fixture.app.queueLimits, semanticReconcileInterval: time.Minute}
	if err := second.registerSemanticJobs(queue.NewRegistry()); err != nil {
		t.Fatal(err)
	}
	if err := second.startSemanticJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fixture.jobs(t, semanticReconcileJobType); got != 1 {
		t.Fatalf("startup reconcile jobs=%d want=1", got)
	}
}

func TestSemanticStartupDoesNotScheduleFullScansByDefault(t *testing.T) {
	fixture := newSemanticJobFixture(t, nil)
	if err := fixture.app.startSemanticJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fixture.jobs(t, semanticReconcileJobType); got != 0 {
		t.Fatalf("default startup reconcile jobs=%d want=0", got)
	}
}

func TestSemanticReconcileSchedulesItsDurableSuccessor(t *testing.T) {
	ctx := context.Background()
	fixture := newSemanticJobFixture(t, nil)
	fixture.app.semanticReconcileInterval = time.Minute
	payload := semanticJob{Model: string(semanticJobModelID()), Index: "related"}
	if err := fixture.app.runSemanticReconcile(ctx, queue.Job[semanticJob]{ID: "reconcile-current", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var row struct {
		Dedupe      string `db:"dedupe_key"`
		AvailableAt int64  `db:"available_at"`
		EnqueuedAt  int64  `db:"enqueued_at"`
	}
	if err := fixture.database.Get(&row, `SELECT "dedupe_key","available_at","enqueued_at" FROM "golem_queue" WHERE "type"=?`, semanticReconcileJobType); err != nil {
		t.Fatal(err)
	}
	want := semanticJobKey(semanticReconcileJobType, payload) + ":reconcile-current"
	if row.Dedupe != want || row.AvailableAt-row.EnqueuedAt != time.Minute.Microseconds() {
		t.Fatalf("scheduled reconcile=%#v want key=%q", row, want)
	}
}

func TestSemanticReconcileSchedulesItsSuccessorBeforeRefreshFailure(t *testing.T) {
	ctx := context.Background()
	fixture := newSemanticJobFixture(t, nil)
	fixture.app.semanticReconcileInterval = time.Minute
	if _, err := fixture.database.ExecContext(ctx, `DROP TABLE "posts"`); err != nil {
		t.Fatal(err)
	}
	payload := semanticJob{Model: string(semanticJobModelID()), Index: "related"}
	if err := fixture.app.runSemanticReconcile(ctx, queue.Job[semanticJob]{ID: "reconcile-failed", Payload: payload}); err == nil {
		t.Fatal("failed refresh returned success")
	}
	if got := fixture.jobs(t, semanticReconcileJobType); got != 1 {
		t.Fatalf("scheduled successors after refresh failure=%d want=1", got)
	}
}

// TestSemanticDrainJobChainsWorkThatArrivedUnderItsLease is the lost wakeup.
// The mark committed mid-pass enqueued against this job's own dedupe key while
// it was leased, so the queue swallowed it; only the job's own successor
// carries that record forward.
func TestSemanticDrainJobChainsWorkThatArrivedUnderItsLease(t *testing.T) {
	ctx := context.Background()
	armed, fired := false, false
	var fixture semanticJobFixture
	fixture = newSemanticJobFixture(t, func(database *sqlx.DB) {
		if !armed || fired {
			return
		}
		fired = true
		key, err := semantickey.Encode([]any{"b"})
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := database.Exec(`UPDATE "posts" SET title='beta revised' WHERE id='b'`); err != nil {
			t.Error(err)
			return
		}
		if _, err := database.Exec(`UPDATE "`+semanticJobStateTable+`" SET status='pending' WHERE record_key=?`, key); err != nil {
			t.Error(err)
			return
		}
	})
	if _, err := fixture.database.Exec(`UPDATE "posts" SET title='alpha revised' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	fixture.mark(t, "a")
	armed = true
	payload := semanticJob{Model: string(semanticJobModelID()), Index: "related"}
	if err := fixture.app.runSemanticDrain(ctx, queue.Job[semanticJob]{ID: "drain-under-lease", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the mid-pass mark never ran")
	}
	var chained int
	if err := fixture.database.Get(&chained, `SELECT COUNT(*) FROM "golem_queue" WHERE "type"=? AND "dedupe_key"=?`,
		semanticDrainJobType, semanticJobKey(semanticDrainJobType, payload)+":drain-under-lease"); err != nil {
		t.Fatal(err)
	}
	if chained != 1 {
		t.Fatalf("chained drain jobs=%d want=1", chained)
	}
	if err := fixture.app.runSemanticDrain(ctx, queue.Job[semanticJob]{ID: "drain-successor", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var status string
	key, err := semantickey.Encode([]any{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.Get(&status, `SELECT status FROM "`+semanticJobStateTable+`" WHERE record_key=?`, key); err != nil {
		t.Fatal(err)
	}
	if status != "ready" {
		t.Fatalf("record marked under the lease status=%q", status)
	}
}

// TestSemanticDrainEnqueueIsDedupedPerIndex is the contract the write path
// enqueues against: writing a row is the request to refresh it, and repeated
// requests coalesce into one outstanding job per index.
func TestSemanticDrainEnqueueIsDedupedPerIndex(t *testing.T) {
	ctx := context.Background()
	fixture := newSemanticJobFixture(t, nil)
	if !fixture.app.semanticIndexed(semanticJobModel) || fixture.app.semanticIndexed(golem.ModelID{0x02}) {
		t.Fatal("semantic index membership is wrong")
	}
	for range 3 {
		if err := fixture.app.enqueueSemanticDrains(ctx, nil, semanticJobModel); err != nil {
			t.Fatal(err)
		}
	}
	if got := fixture.jobs(t, semanticDrainJobType); got != 1 {
		t.Fatalf("drain jobs=%d want=1", got)
	}
	if err := fixture.app.enqueueSemanticDrains(ctx, nil, golem.ModelID{0x02}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.jobs(t, semanticDrainJobType); got != 1 {
		t.Fatalf("unrelated model enqueued a drain: jobs=%d", got)
	}
}

func TestSemanticDrainLeasedCollisionCreatesTransactionalContinuation(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newSemanticJobFixture(t, nil)
			payload := semanticJob{Model: string(semanticJobModelID()), Index: "related"}
			base := semanticJobKey(semanticDrainJobType, payload)
			if _, err := fixture.app.enqueueSemanticJob(ctx, nil, fixture.app.semanticDrain, payload, base); err != nil {
				t.Fatal(err)
			}
			claimed, err := fixture.app.queueStore.Claim(ctx, queueprovider.ClaimOptions{Types: []string{semanticDrainJobType}, Limit: 1, LeaseDuration: 5 * time.Minute})
			if err != nil || len(claimed) != 1 {
				t.Fatalf("claim=%#v error=%v", claimed, err)
			}
			transaction, err := fixture.database.BeginTxx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.app.enqueueSemanticDrains(ctx, transaction, semanticJobModel); err != nil {
				_ = transaction.Rollback()
				t.Fatal(err)
			}
			if commit {
				err = transaction.Commit()
			} else {
				err = transaction.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			var active int
			if err := fixture.database.Get(&active, `SELECT COUNT(*) FROM "golem_queue" WHERE "dedupe_key"=? AND "status" IN ('pending','leased')`, base+":"+claimed[0].ID); err != nil {
				t.Fatal(err)
			}
			want := 0
			if commit {
				want = 1
			}
			if active != want {
				t.Fatalf("active continuations=%d want=%d", active, want)
			}
		})
	}
}

// TestSemanticJobForAnAbsentIndexFailsWithoutRetrying covers a job left behind
// by a schema that dropped its index: no number of attempts can make the index
// reappear, so the attempt budget is not spent on it.
func TestSemanticJobForAnAbsentIndexFailsWithoutRetrying(t *testing.T) {
	ctx := context.Background()
	fixture := newSemanticJobFixture(t, nil)
	payload := semanticJob{Model: string(semanticJobModelID()), Index: "gone"}
	for name, run := range map[string]func(context.Context, queue.Job[semanticJob]) error{
		semanticDrainJobType:     fixture.app.runSemanticDrain,
		semanticReconcileJobType: fixture.app.runSemanticReconcile,
	} {
		outcome := queue.Classify(run(ctx, queue.Job[semanticJob]{ID: "absent", Payload: payload}))
		if outcome.Resolution != queue.ResolutionFailed {
			t.Fatalf("%s for an absent index resolution=%q want=%q", name, outcome.Resolution, queue.ResolutionFailed)
		}
	}
}
