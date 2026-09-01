package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

type deterministicProvider struct {
	specification embedding.Specification
	mu            sync.Mutex
	inputs        []string
	keys          []string
	panicEmbed    bool
	rejectText    string
	beforeEmbed   func(*sqlx.DB)
	database      *sqlx.DB
}

type semanticObservation struct {
	kind       observe.Kind
	operation  observe.Operation
	outcome    observe.Outcome
	reason     observe.Reason
	statements int
	aggregate  int64
}

type semanticObservationCollector struct {
	mu     sync.Mutex
	values []semanticObservation
}

type semanticLockCheckingObserver struct {
	manager   *Manager
	collector *semanticObservationCollector
	mu        sync.Mutex
	blocked   bool
}

func (observer *semanticLockCheckingObserver) ObserveGolem(ctx context.Context, value observe.Observation) {
	if !observer.manager.mu.TryLock() {
		observer.mu.Lock()
		observer.blocked = true
		observer.mu.Unlock()
	} else {
		observer.manager.mu.Unlock()
	}
	observer.collector.ObserveGolem(ctx, value)
}

func (observer *semanticLockCheckingObserver) wasBlocked() bool {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.blocked
}

func (collector *semanticObservationCollector) ObserveGolem(_ context.Context, value observe.Observation) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.values = append(collector.values, semanticObservation{
		kind: value.Kind(), operation: value.Operation(), outcome: value.Outcome(), reason: value.Reason(),
		statements: value.StatementCount(), aggregate: value.AggregateCount(),
	})
}

func (collector *semanticObservationCollector) take() []semanticObservation {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	result := append([]semanticObservation(nil), collector.values...)
	collector.values = nil
	return result
}

func (provider *deterministicProvider) Specification() embedding.Specification {
	return provider.specification
}
func (provider *deterministicProvider) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	if provider.panicEmbed {
		panic("secret embedding provider panic")
	}
	if provider.beforeEmbed != nil {
		provider.beforeEmbed(provider.database)
	}
	// A real provider refuses the whole request when one document is over its
	// limit, so the refusal names no record and the drain has to isolate it.
	if provider.rejectText != "" {
		for _, input := range inputs {
			if strings.Contains(input.Text(), provider.rejectText) {
				return nil, fmt.Errorf("provider refused a document")
			}
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	result := make([]embedding.Vector, len(inputs))
	for index, input := range inputs {
		provider.inputs = append(provider.inputs, input.Text())
		provider.keys = append(provider.keys, input.Key())
		values := []float32{0, 0, 1}
		if strings.Contains(input.Text(), "alpha") {
			values = []float32{1, 0, 0}
		}
		if strings.Contains(input.Text(), "beta") {
			values = []float32{0, 1, 0}
		}
		result[index], _ = embedding.NewVector(values)
	}
	return result, nil
}

func TestManagerContainsEmbeddingProviderPanic(t *testing.T) {
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 8)
	provider := &deterministicProvider{specification: specification, panicEmbed: true}
	_, err := callEmbeddingProvider(context.Background(), provider, []embedding.Input{{}})
	if err == nil || err.Error() != "embedding provider panicked" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("panic boundary error=%v", err)
	}
}

type secretFailingProvider struct{ specification embedding.Specification }

func (provider secretFailingProvider) Specification() embedding.Specification {
	return provider.specification
}
func (provider secretFailingProvider) Embed(context.Context, []embedding.Input) ([]embedding.Vector, error) {
	return nil, fmt.Errorf("credential=semantic-provider-canary")
}

type unavailableProvider struct {
	specification embedding.Specification
	calls         int
}

func (provider *unavailableProvider) Specification() embedding.Specification {
	return provider.specification
}

func (provider *unavailableProvider) Embed(context.Context, []embedding.Input) ([]embedding.Vector, error) {
	provider.calls++
	return nil, embedding.NewError(embedding.CodeUnavailable, fmt.Errorf("credential=semantic-unavailable-canary"))
}

func TestManagerRedactsEmbeddingProviderErrorCause(t *testing.T) {
	specification, _ := embedding.NewSpecification("test", "private-model", "v1", 3, 8)
	_, privateErr := callEmbeddingProvider(context.Background(), secretFailingProvider{specification: specification}, []embedding.Input{{}})
	publicErr := embedding.NewError(embedding.CodeProvider, privateErr)
	if privateErr == nil || strings.Contains(publicErr.Error(), "canary") || strings.Contains(fmt.Sprint(errors.Unwrap(publicErr)), "canary") {
		t.Fatalf("provider error crossed private boundary: %v cause=%v", publicErr, errors.Unwrap(publicErr))
	}
}

func TestManagerPreservesUnavailableCodeAcrossProviderRedaction(t *testing.T) {
	specification, _ := embedding.NewSpecification("test", "private-model", "v1", 3, 8)
	provider := &unavailableProvider{specification: specification}
	_, err := callEmbeddingProvider(context.Background(), provider, []embedding.Input{{}})
	if code, ok := embedding.CodeOf(err); !ok || code != embedding.CodeUnavailable || strings.Contains(err.Error(), "canary") || strings.Contains(fmt.Sprint(errors.Unwrap(err)), "canary") {
		t.Fatalf("unavailable provider failure was not closed: error=%v cause=%v", err, errors.Unwrap(err))
	}
}

func TestSemanticObservationAndErrorRedactProviderCanary(t *testing.T) {
	specification, _ := embedding.NewSpecification("test", "private-model", "v1", 3, 8)
	collector := &semanticObservationCollector{}
	manager := &Manager{
		provider: ir.SQLite,
		observer: collector,
		indexes: []Index{{
			Descriptor:    semanticstorage.Descriptor{ModelID: "post", Name: "related", Identity: textIdentity()},
			Provider:      secretFailingProvider{specification: specification},
			Specification: specification,
		}},
	}
	_, err := manager.Query(context.Background(), "post", "related", "query canary", textCandidates(`SELECT "id" AS "id" FROM "posts"`), 1)
	if code, ok := embedding.CodeOf(err); !ok || code != embedding.CodeProvider || strings.Contains(err.Error(), "canary") || strings.Contains(fmt.Sprint(errors.Unwrap(err)), "canary") {
		t.Fatalf("public provider failure was not closed: error=%v cause=%v", err, errors.Unwrap(err))
	}
	assertSemanticObservations(t, collector.take(), []semanticObservation{
		{kind: observe.KindSemantic, operation: observe.OperationSemanticProvider, outcome: observe.OutcomeFailure, reason: observe.ReasonProvider, statements: 0, aggregate: 1},
		{kind: observe.KindSemantic, operation: observe.OperationSemanticRank, outcome: observe.OutcomeFailure, reason: observe.ReasonProvider, statements: 0, aggregate: 0},
	})
}
func (provider *deterministicProvider) calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return len(provider.inputs)
}

func (provider *deterministicProvider) texts() []string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]string(nil), provider.inputs...)
}

func (provider *deterministicProvider) correlationKeys() []string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]string(nil), provider.keys...)
}

func TestManagerSelectedRefreshNeverTouchesUnrelatedIndex(t *testing.T) {
	ctx := context.Background()
	database, err := sqlitevec.Open("file:" + t.TempDir() + "/selected.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	db := sqlx.NewDb(database, "sqlite3")
	if _, err := db.Exec(`
CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT);
CREATE TABLE "other" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT);
CREATE TABLE "_golem_semantic_semantic-post-related_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL) STRICT;
CREATE VIRTUAL TABLE "_golem_semantic_semantic-post-related_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine);
CREATE TABLE "_golem_semantic_semantic-other-unrelated_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL) STRICT;
CREATE VIRTUAL TABLE "_golem_semantic_semantic-other-unrelated_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine);
INSERT INTO "posts" (id,title) VALUES ('a','alpha');
INSERT INTO "other" (id,title) VALUES ('b','beta');`); err != nil {
		t.Fatal(err)
	}

	schema := semanticSchema(t, 3)
	schema.Namespace = physical.Namespace{Name: "main"}
	schema.Tables = []physical.PhysicalTable{
		{
			ID: "post", Name: "posts",
			Columns: []physical.PhysicalColumn{
				{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
				{ID: "title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			},
			PrimaryKey: &physical.PhysicalKey{ID: "post-primary", Name: "pk_posts", Columns: []ir.FieldID{"id"}},
		},
		{
			ID: "other", Name: "other",
			Columns: []physical.PhysicalColumn{
				{ID: "other-id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
				{ID: "other-title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			},
			PrimaryKey: &physical.PhysicalKey{ID: "other-primary", Name: "pk_other", Columns: []ir.FieldID{"other-id"}},
		},
	}
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 8)
	selected := &deterministicProvider{specification: specification}
	unrelated := &deterministicProvider{specification: specification, panicEmbed: true}
	manager := &Manager{
		database: db, provider: ir.SQLite, schema: schema,
		indexes: []Index{
			{
				Descriptor: semanticstorage.Descriptor{ModelID: "post", Name: "related", Storage: "_golem_semantic_semantic-post-related", Fields: []ir.FieldID{"title"}, Identity: textIdentity()},
				Provider:   selected, Specification: specification,
			},
			{
				Descriptor: semanticstorage.Descriptor{ModelID: "other", Name: "unrelated", Storage: "_golem_semantic_semantic-other-unrelated", Fields: []ir.FieldID{"other-title"}, Identity: []semanticstorage.IdentityColumn{{Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, NotNull: true}}},
				Provider:   unrelated, Specification: specification,
			},
		},
	}
	if err := manager.Refresh(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if selected.calls() != 1 || unrelated.calls() != 0 {
		t.Fatalf("selected calls=%d unrelated calls=%d", selected.calls(), unrelated.calls())
	}
}

func TestManagerRefreshesOnlyChangedSQLiteSourcesAndRemovesDeletedRows(t *testing.T) {
	ctx := context.Background()
	database, err := sqlitevec.Open("file:" + t.TempDir() + "/semantic.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	db := sqlx.NewDb(database, "sqlite3")
	if _, err := db.Exec(`CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT); CREATE TABLE "_golem_semantic_semantic-post-related_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL) STRICT; CREATE VIRTUAL TABLE "_golem_semantic_semantic-post-related_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO "posts" (id,title) VALUES ('a','alpha'),('b','beta')`); err != nil {
		t.Fatal(err)
	}

	schema := semanticSchema(t, 3)
	schema.Namespace = physical.Namespace{Name: "main"}
	schema.Tables = []physical.PhysicalTable{{
		ID:   "post",
		Name: "posts",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "post-primary", Name: "pk_posts", Columns: []ir.FieldID{"id"}},
	}}
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 8)
	embedder := &deterministicProvider{specification: specification}
	registry, _ := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	inventory, err := NewInventory(schema, registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(db, ir.SQLite, schema, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if embedder.calls() != 2 {
		t.Fatalf("embedded=%d want=2", embedder.calls())
	}
	keys := embedder.correlationKeys()
	if len(keys) != 2 || keys[0] != "source-0" || keys[1] != "source-1" {
		t.Fatalf("provider correlation keys=%q want batch-local opaque ordinals", keys)
	}
	for _, key := range keys {
		if strings.Contains(key, "a") || strings.Contains(key, "b") || strings.Contains(key, "golem-semantic-key") {
			t.Fatalf("provider correlation key disclosed database identity: %q", key)
		}
	}
	if err := manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if embedder.calls() != 2 {
		t.Fatalf("unchanged rows were re-embedded: %d", embedder.calls())
	}

	query, _ := sqlitevec.Serialize([]float32{1, 0, 0}, 3)
	var nearest string
	if err := db.Get(&nearest, `SELECT record_key FROM "_golem_semantic_semantic-post-related_vec" WHERE embedding MATCH ? AND k=1 ORDER BY distance`, query); err != nil {
		t.Fatal(err)
	}
	if nearest != "golem-semantic-key:v1|5:s:1:a" {
		t.Fatalf("nearest=%q", nearest)
	}
	betaKey, _ := semantickey.Encode([]any{"b"})
	ranked, err := manager.Query(ctx, "post", "related", "alpha", textCandidates(`SELECT "id" AS "id" FROM "posts" WHERE "id"=?`, "b"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 1 || ranked[0].Key != betaKey || ranked[0].Distance < 0.99 || ranked[0].Distance > 1.01 {
		t.Fatalf("authorized ranking=%#v", ranked)
	}

	if _, err := db.Exec(`UPDATE "posts" SET title='alpha revised' WHERE id='a'; DELETE FROM "posts" WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if embedder.calls() != 4 {
		t.Fatalf("changed rows embedded=%d want=4 (including query)", embedder.calls())
	}
	for _, table := range []string{"_state", "_vec"} {
		var count int
		name := "_golem_semantic_semantic-post-related" + table
		if err := db.Get(&count, fmt.Sprintf(`SELECT COUNT(*) FROM %q`, name)); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s count=%d", table, count)
		}
	}
}

func TestSemanticObservationCountsSQLiteRefreshProviderAndRank(t *testing.T) {
	ctx := context.Background()
	database, err := sqlitevec.Open("file:" + t.TempDir() + "/observed.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	db := sqlx.NewDb(database, "sqlite3")
	if _, err := db.Exec(`CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT); CREATE TABLE "_golem_semantic_semantic-post-related_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL) STRICT; CREATE VIRTUAL TABLE "_golem_semantic_semantic-post-related_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine); INSERT INTO "posts" (id,title) VALUES ('a','alpha'),('b','beta')`); err != nil {
		t.Fatal(err)
	}
	schema := semanticSchema(t, 3)
	schema.Namespace = physical.Namespace{Name: "main"}
	schema.Tables = []physical.PhysicalTable{{
		ID: "post", Name: "posts",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "post-primary", Name: "pk_posts", Columns: []ir.FieldID{"id"}},
	}}
	specification, _ := embedding.NewSpecification("test", "private-model", "v1", 3, 8)
	embedder := &deterministicProvider{specification: specification}
	registry, _ := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	inventory, err := NewInventory(schema, registry)
	if err != nil {
		t.Fatal(err)
	}
	collector := &semanticObservationCollector{}
	manager, err := NewManager(db, ir.SQLite, schema, inventory)
	if err != nil {
		t.Fatal(err)
	}
	lockObserver := &semanticLockCheckingObserver{manager: manager, collector: collector}
	manager.observer = lockObserver
	if err := manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	assertSemanticObservations(t, collector.take(), []semanticObservation{
		{kind: observe.KindSemantic, operation: observe.OperationSemanticProvider, outcome: observe.OutcomeSuccess, reason: observe.ReasonNone, statements: 0, aggregate: 2},
		{kind: observe.KindSemantic, operation: observe.OperationSemanticRefresh, outcome: observe.OutcomeSuccess, reason: observe.ReasonNone, statements: 8, aggregate: 2},
	})
	if lockObserver.wasBlocked() {
		t.Fatal("semantic observer was invoked while the refresh mutex was held")
	}

	if err := manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	assertSemanticObservations(t, collector.take(), []semanticObservation{
		{kind: observe.KindSemantic, operation: observe.OperationSemanticRefresh, outcome: observe.OutcomeSuccess, reason: observe.ReasonNone, statements: 2, aggregate: 0},
	})

	if _, err := manager.Query(ctx, "post", "related", "alpha", textCandidates(`SELECT "id" AS "id" FROM "posts" WHERE "id"=?`, "b"), 1); err != nil {
		t.Fatal(err)
	}
	assertSemanticObservations(t, collector.take(), []semanticObservation{
		{kind: observe.KindSemantic, operation: observe.OperationSemanticProvider, outcome: observe.OutcomeSuccess, reason: observe.ReasonNone, statements: 0, aggregate: 1},
		{kind: observe.KindSemantic, operation: observe.OperationSemanticRank, outcome: observe.OutcomeSuccess, reason: observe.ReasonNone, statements: 1, aggregate: 1},
	})

	// Similarity reads the source vector and ranks. Freshness is not consulted,
	// so it is two statements and no provider record at all.
	sourceKey, err := semantickey.Encode([]any{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.QueryByKey(ctx, "post", "related", sourceKey, textCandidates(`SELECT "id" AS "id" FROM "posts" WHERE "id"=?`, "b"), 1); err != nil {
		t.Fatal(err)
	}
	assertSemanticObservations(t, collector.take(), []semanticObservation{
		{kind: observe.KindSemantic, operation: observe.OperationSemanticRank, outcome: observe.OutcomeSuccess, reason: observe.ReasonNone, statements: 2, aggregate: 1},
	})
}

func assertSemanticObservations(t *testing.T, got, want []semanticObservation) {
	t.Helper()
	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("semantic observations=%#v want=%#v", got, want)
	}
}

type textIdentityScan struct{ id sql.NullString }

func (scan *textIdentityScan) Destinations() []any { return []any{&scan.id} }
func (scan *textIdentityScan) RawValues() []any {
	if !scan.id.Valid {
		return []any{nil}
	}
	return []any{scan.id.String}
}

func textIdentity() []semanticstorage.IdentityColumn {
	return []semanticstorage.IdentityColumn{{Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, NotNull: true}}
}

func textCandidates(statement string, args ...any) Candidates {
	return Candidates{
		SQL: statement, Args: args, Columns: []string{"id"},
		MaxStatementBytes: readsql.MaxStatementBytes, MaxStatementAliases: readsql.MaxStatementAliases,
		NewScan: func() IdentityScan { return &textIdentityScan{} },
	}
}

func TestRankStatementAppliesLimitsAfterAddingSemanticSQL(t *testing.T) {
	fixture := newDrainFixture(t)
	index, ok := fixture.manager.index("post", "related")
	if !ok {
		t.Fatal("semantic index is absent")
	}
	base := textCandidates(`SELECT "id" AS "id" FROM "posts" WHERE "id"=?`, "a")
	tests := []struct {
		name    string
		bytes   int
		aliases int
	}{
		{name: "bytes", bytes: len(base.SQL), aliases: readsql.MaxStatementAliases},
		{name: "aliases", bytes: readsql.MaxStatementBytes, aliases: strings.Count(base.SQL, " AS ")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates := base
			candidates.MaxStatementBytes = test.bytes
			candidates.MaxStatementAliases = test.aliases
			if err := readsql.ValidateStatementComplexity(candidates.Model, candidates.SQL, test.bytes, test.aliases); err != nil {
				t.Fatalf("candidate statement exceeded configured limit: %v", err)
			}
			_, err := fixture.manager.rankVector(context.Background(), index, []byte{0}, candidates, "", 1)
			if code, ok := embedding.CodeOf(err); !ok || code != embedding.CodeInvalidInput {
				t.Fatalf("rank error=%v code=%q ok=%t", err, code, ok)
			}
		})
	}
}

type drainFixture struct {
	db       *sqlx.DB
	manager  *Manager
	embedder *deterministicProvider
}

const drainStateTable = "_golem_semantic_semantic-post-related_state"
const drainVectorTable = "_golem_semantic_semantic-post-related_vec"

func newDrainFixture(t *testing.T) drainFixture {
	t.Helper()
	return newDrainFixtureWith(t, nil)
}

func newDrainFixtureWith(t *testing.T, hook func(*sqlx.DB)) drainFixture {
	t.Helper()
	database, err := sqlitevec.Open("file:" + t.TempDir() + "/drain.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	db := sqlx.NewDb(database, "sqlite3")
	if _, err := db.Exec(`
CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT);
CREATE TABLE "` + drainStateTable + `" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL,
  CHECK (status IN ('pending','ready','failed'))) STRICT;
CREATE INDEX "_golem_semantic_semantic-post-related_state_stale" ON "` + drainStateTable + `" ("record_key" ASC) WHERE "status" <> 'ready';
CREATE VIRTUAL TABLE "` + drainVectorTable + `" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine);
INSERT INTO "posts" (id,title) VALUES ('a','alpha'),('b','beta')`); err != nil {
		t.Fatal(err)
	}
	schema := semanticSchema(t, 3)
	schema.Namespace = physical.Namespace{Name: "main"}
	schema.Tables = []physical.PhysicalTable{{
		ID: "post", Name: "posts",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "post-primary", Name: "pk_posts", Columns: []ir.FieldID{"id"}},
	}}
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 8)
	embedder := &deterministicProvider{specification: specification, beforeEmbed: hook, database: db}
	registry, _ := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	inventory, err := NewInventory(schema, registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(db, ir.SQLite, schema, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if embedder.calls() != 2 {
		t.Fatalf("seed embedded=%d want=2", embedder.calls())
	}
	return drainFixture{db: db, manager: manager, embedder: embedder}
}

func (fixture drainFixture) mark(t *testing.T, id string) {
	t.Helper()
	key, err := semantickey.Encode([]any{id})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.db.Exec(`UPDATE "`+drainStateTable+`" SET status='pending' WHERE record_key=?`, key)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("mark %q affected=%d", id, affected)
	}
}

func (fixture drainFixture) status(t *testing.T, id string) string {
	t.Helper()
	key, err := semantickey.Encode([]any{id})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := fixture.db.Get(&status, `SELECT status FROM "`+drainStateTable+`" WHERE record_key=?`, key); err != nil {
		t.Fatal(err)
	}
	return status
}

// markStale marks through the real write path, which is the only path that
// rewrites updated_at. A test that marks by hand leaves updated_at alone and
// would agree with a drain that clobbers the mark it never saw.
func (fixture drainFixture) markStale(t *testing.T, ids ...string) {
	t.Helper()
	records := make([]MarkRecord, 0, len(ids))
	for _, id := range ids {
		key, err := semantickey.Encode([]any{id})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, MarkRecord{Key: key, Identity: []any{id}})
	}
	if err := fixture.manager.MarkStale(context.Background(), fixture.db, "post", records); err != nil {
		t.Fatal(err)
	}
}

func (fixture drainFixture) hash(t *testing.T, id string) []byte {
	t.Helper()
	key, err := semantickey.Encode([]any{id})
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := fixture.db.Get(&stored, `SELECT source_hash FROM "`+drainStateTable+`" WHERE record_key=?`, key); err != nil {
		t.Fatal(err)
	}
	return stored
}

func (fixture drainFixture) failure(t *testing.T, id string) (string, sql.NullString, int) {
	t.Helper()
	key, err := semantickey.Encode([]any{id})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var code sql.NullString
	var attempts int
	if err := fixture.db.QueryRow(`SELECT status,error_code,attempt_count FROM "`+drainStateTable+`" WHERE record_key=?`, key).Scan(&status, &code, &attempts); err != nil {
		t.Fatal(err)
	}
	return status, code, attempts
}

func (fixture drainFixture) count(t *testing.T, table string) int {
	t.Helper()
	var count int
	if err := fixture.db.Get(&count, `SELECT COUNT(*) FROM "`+table+`"`); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestSemanticDrainFlipsUnchangedMarkedRecordsWithoutAProviderCall proves the
// content hash is what makes marking every write affordable: a mark that turns
// out to describe no change costs no embedding at all.
func TestSemanticDrainFlipsUnchangedMarkedRecordsWithoutAProviderCall(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	fixture.mark(t, "a")
	fixture.mark(t, "b")
	pending, err := fixture.manager.Drain(ctx, "post", "related")
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("drain reported outstanding work after flipping every marked record")
	}
	if fixture.embedder.calls() != 2 {
		t.Fatalf("unchanged marked records embedded: calls=%d want=2", fixture.embedder.calls())
	}
	if fixture.status(t, "a") != "ready" || fixture.status(t, "b") != "ready" {
		t.Fatalf("statuses a=%q b=%q", fixture.status(t, "a"), fixture.status(t, "b"))
	}
}

// TestSemanticDrainReadsOnlyMarkedRecordsAndNotTheOwnerTable changes both rows
// but marks one. A drain that scanned the owner table would notice the other.
func TestSemanticDrainReadsOnlyMarkedRecordsAndNotTheOwnerTable(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='alpha revised' WHERE id='a'; UPDATE "posts" SET title='beta revised' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	fixture.mark(t, "a")
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.embedder.calls() != 3 {
		t.Fatalf("drain embedded=%d want=3 (only the marked record)", fixture.embedder.calls())
	}
	if got := fixture.embedder.texts()[2]; !strings.Contains(got, "alpha revised") {
		t.Fatalf("drain embedded %q, want the marked record's revised document", got)
	}
	if fixture.status(t, "b") != "ready" {
		t.Fatalf("unmarked record status=%q", fixture.status(t, "b"))
	}
}

// TestSemanticDrainRemovesShadowRowsWhoseOwnerIsGone asserts on storage, not on
// results: the rank join already hides an orphaned vector, so only the shadow
// tables can show whether the orphan was actually removed.
func TestSemanticDrainRemovesShadowRowsWhoseOwnerIsGone(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	fixture.mark(t, "b")
	if _, err := fixture.db.Exec(`DELETE FROM "posts" WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.count(t, drainStateTable) != 1 || fixture.count(t, drainVectorTable) != 1 {
		t.Fatalf("state=%d vec=%d want=1,1", fixture.count(t, drainStateTable), fixture.count(t, drainVectorTable))
	}
	if fixture.embedder.calls() != 2 {
		t.Fatalf("owner removal embedded: calls=%d", fixture.embedder.calls())
	}
}

// TestSemanticDrainReportsAMarkThatArrivedDuringItsPass covers the lost wakeup:
// the queue coalesces a mark's enqueue into the drain that is still holding its
// lease, so the drain itself has to hand its successor forward.
func TestSemanticDrainReportsAMarkThatArrivedDuringItsPass(t *testing.T) {
	ctx := context.Background()
	armed, fired := false, false
	fixture := newDrainFixtureWith(t, func(db *sqlx.DB) {
		if !armed || fired {
			return
		}
		fired = true
		key, err := semantickey.Encode([]any{"b"})
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := db.Exec(`UPDATE "posts" SET title='beta revised' WHERE id='b'`); err != nil {
			t.Error(err)
			return
		}
		if _, err := db.Exec(`UPDATE "`+drainStateTable+`" SET status='pending' WHERE record_key=?`, key); err != nil {
			t.Error(err)
		}
	})
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='alpha revised' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	fixture.mark(t, "a")
	armed = true
	pending, err := fixture.manager.Drain(ctx, "post", "related")
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the mid-pass mark never ran")
	}
	if !pending {
		t.Fatal("a mark that arrived during the pass was not reported as outstanding work")
	}
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.status(t, "b") != "ready" || fixture.embedder.calls() != 4 {
		t.Fatalf("chained drain status=%q calls=%d", fixture.status(t, "b"), fixture.embedder.calls())
	}
}

// TestSemanticDrainKeepsAMarkOnTheRecordItIsEmbedding is the same-record lost
// wakeup. The second mark's enqueue is coalesced into the job that is still
// running, so a pass that flips the record ready under the vector it computed
// from the older document hands nothing forward: the record serves that vector
// until some other write happens to touch it.
func TestSemanticDrainKeepsAMarkOnTheRecordItIsEmbedding(t *testing.T) {
	ctx := context.Background()
	var fixture drainFixture
	armed, fired := false, false
	fixture = newDrainFixtureWith(t, func(db *sqlx.DB) {
		if !armed || fired {
			return
		}
		fired = true
		if _, err := db.Exec(`UPDATE "posts" SET title='alpha third' WHERE id='a'`); err != nil {
			t.Error(err)
			return
		}
		fixture.markStale(t, "a")
	})
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='alpha revised' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "a")
	before := fixture.hash(t, "a")
	armed = true
	pending, err := fixture.manager.Drain(ctx, "post", "related")
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the mid-pass mark never ran")
	}
	if fixture.status(t, "a") != "pending" {
		t.Fatalf("the pass erased a mark on the record it was embedding: status=%q", fixture.status(t, "a"))
	}
	if !equalBytes(before, fixture.hash(t, "a")) {
		t.Fatal("the pass stored the hash of the document it read before the record changed again")
	}
	if !pending {
		t.Fatal("a record re-marked during the pass was not reported as outstanding work")
	}
	pending, err = fixture.manager.Drain(ctx, "post", "related")
	if err != nil {
		t.Fatal(err)
	}
	if pending || fixture.status(t, "a") != "ready" || equalBytes(before, fixture.hash(t, "a")) {
		t.Fatalf("chained pass pending=%t status=%q hash unchanged=%t", pending, fixture.status(t, "a"), equalBytes(before, fixture.hash(t, "a")))
	}
	texts := fixture.embedder.texts()
	if !strings.Contains(texts[len(texts)-1], "alpha third") {
		t.Fatalf("chained pass embedded %q, want the document the mid-pass write left behind", texts[len(texts)-1])
	}
}

// TestSemanticDrainKeepsAMarkOnTheRecordItIsFlipping is the same window on the
// cheap path. A record whose document did not change is settled without a
// provider call, but that flip still has to lose to a mark committed after the
// pass read the record — otherwise the write that changed it is erased by a
// pass that never saw the change.
func TestSemanticDrainKeepsAMarkOnTheRecordItIsFlipping(t *testing.T) {
	ctx := context.Background()
	var fixture drainFixture
	armed, fired := false, false
	fixture = newDrainFixtureWith(t, func(db *sqlx.DB) {
		if !armed || fired {
			return
		}
		fired = true
		if _, err := db.Exec(`UPDATE "posts" SET title='beta revised' WHERE id='b'`); err != nil {
			t.Error(err)
			return
		}
		fixture.markStale(t, "b")
	})
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='alpha revised' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "a", "b")
	armed = true
	pending, err := fixture.manager.Drain(ctx, "post", "related")
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the mid-pass mark never ran")
	}
	if fixture.status(t, "b") != "pending" {
		t.Fatalf("the unchanged flip erased a mark that arrived during the pass: status=%q", fixture.status(t, "b"))
	}
	if !pending {
		t.Fatal("a record re-marked during the pass was not reported as outstanding work")
	}
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.status(t, "b") != "ready" {
		t.Fatalf("chained pass status=%q", fixture.status(t, "b"))
	}
	texts := fixture.embedder.texts()
	if !strings.Contains(texts[len(texts)-1], "beta revised") {
		t.Fatalf("chained pass embedded %q, want the mid-pass write", texts[len(texts)-1])
	}
}

// TestSemanticDrainKeepsARecordRecreatedDuringItsPass is the third shape of the
// same window. Cleanup is decided from an owner read that happened before the
// provider call, so a record deleted, probed, and created again while the pass
// ran must not have the state row its re-creation just wrote removed.
func TestSemanticDrainKeepsARecordRecreatedDuringItsPass(t *testing.T) {
	ctx := context.Background()
	var fixture drainFixture
	armed, fired := false, false
	fixture = newDrainFixtureWith(t, func(db *sqlx.DB) {
		if !armed || fired {
			return
		}
		fired = true
		if _, err := db.Exec(`INSERT INTO "posts" (id,title) VALUES ('b','beta reborn')`); err != nil {
			t.Error(err)
			return
		}
		fixture.markStale(t, "b")
	})
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='alpha revised' WHERE id='a'; DELETE FROM "posts" WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "a", "b")
	armed = true
	pending, err := fixture.manager.Drain(ctx, "post", "related")
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the mid-pass re-creation never ran")
	}
	if fixture.count(t, drainStateTable) != 2 || fixture.count(t, drainVectorTable) != 2 {
		t.Fatalf("the pass removed a record re-created while it ran: state=%d vec=%d want=2,2", fixture.count(t, drainStateTable), fixture.count(t, drainVectorTable))
	}
	if fixture.status(t, "b") != "pending" || !pending {
		t.Fatalf("re-created record status=%q pending=%t", fixture.status(t, "b"), pending)
	}
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.status(t, "b") != "ready" {
		t.Fatalf("chained pass status=%q", fixture.status(t, "b"))
	}
	texts := fixture.embedder.texts()
	if !strings.Contains(texts[len(texts)-1], "beta reborn") {
		t.Fatalf("chained pass embedded %q, want the re-created record", texts[len(texts)-1])
	}
}

// TestSemanticDrainQuarantinesTheDocumentAProviderRefuses is the poisoned batch.
// One document the provider will not accept must not hold its own batch, every
// later batch, and every later pass hostage — and it must not be probed forever
// either, because the drain chains a successor for as long as anything is stale.
func TestSemanticDrainQuarantinesTheDocumentAProviderRefuses(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	const poisoned = "p02"
	ids := make([]string, 0, 12)
	for ordinal := range 12 {
		id := fmt.Sprintf("p%02d", ordinal)
		title := "gamma " + id
		if id == poisoned {
			title = "poison " + id
		}
		if _, err := fixture.db.Exec(`INSERT INTO "posts" (id,title) VALUES (?,?)`, id, title); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	fixture.markStale(t, ids...)
	fixture.embedder.rejectText = "poison"
	seeded := fixture.embedder.calls()
	pending, err := fixture.manager.Drain(ctx, "post", "related")
	if err != nil {
		t.Fatalf("one refused document failed the whole pass: %v", err)
	}
	if pending {
		t.Fatal("a quarantined record kept the drain chaining a successor for work no pass can finish")
	}
	if got := fixture.embedder.calls() - seeded; got != len(ids)-1 {
		t.Fatalf("embedded=%d want=%d (every marked record but the refused one)", got, len(ids)-1)
	}
	for _, id := range ids {
		if id == poisoned {
			continue
		}
		if fixture.status(t, id) != "ready" {
			t.Fatalf("record %q status=%q — the refusal was not contained to the document that caused it", id, fixture.status(t, id))
		}
	}
	if fixture.count(t, drainVectorTable) != len(ids)+1 {
		t.Fatalf("vectors=%d want=%d", fixture.count(t, drainVectorTable), len(ids)+1)
	}
	status, code, attempts := fixture.failure(t, poisoned)
	if status != "failed" || !code.Valid || code.String == "" || attempts != 1 {
		t.Fatalf("quarantined record status=%q error_code=%#v attempt_count=%d", status, code, attempts)
	}
	if pending, err = fixture.manager.Drain(ctx, "post", "related"); err != nil || pending {
		t.Fatalf("second pass pending=%t error=%v", pending, err)
	}
	if got := fixture.embedder.calls() - seeded; got != len(ids)-1 {
		t.Fatalf("a quarantined record was sent to the provider again: embedded=%d", got)
	}
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='gamma '||id WHERE id=?`, poisoned); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, poisoned)
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.status(t, poisoned) != "ready" {
		t.Fatalf("a write to a quarantined record did not retry it: status=%q", fixture.status(t, poisoned))
	}
}

func TestSemanticDrainRetriesUnavailableProviderWithoutQuarantine(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title=title||' revised'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "a", "b")
	unavailable := &unavailableProvider{specification: fixture.embedder.specification}
	fixture.manager.indexes[0].Provider = unavailable
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err == nil {
		t.Fatal("unavailable provider did not fail the drain")
	} else if code, ok := embedding.CodeOf(err); !ok || code != embedding.CodeUnavailable || strings.Contains(err.Error(), "canary") || strings.Contains(fmt.Sprint(errors.Unwrap(err)), "canary") {
		t.Fatalf("unavailable drain error=%v cause=%v", err, errors.Unwrap(err))
	}
	if unavailable.calls != 1 {
		t.Fatalf("unavailable batch calls=%d want=1", unavailable.calls)
	}
	if fixture.status(t, "a") != "pending" || fixture.status(t, "b") != "pending" {
		t.Fatalf("unavailable records were quarantined: a=%q b=%q", fixture.status(t, "a"), fixture.status(t, "b"))
	}
	fixture.manager.indexes[0].Provider = fixture.embedder
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.status(t, "a") != "ready" || fixture.status(t, "b") != "ready" {
		t.Fatalf("retry did not settle records: a=%q b=%q", fixture.status(t, "a"), fixture.status(t, "b"))
	}
}

func TestSemanticReconcileQuarantinesAnUnseenRefusedRecord(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO "posts" (id,title) VALUES ('c','poison c')`); err != nil {
		t.Fatal(err)
	}
	fixture.embedder.rejectText = "poison"
	if err := fixture.manager.Refresh(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	status, code, attempts := fixture.failure(t, "c")
	if status != "failed" || !code.Valid || code.String != string(embedding.CodeProvider) || attempts != 1 {
		t.Fatalf("unseen quarantine status=%q code=%#v attempts=%d", status, code, attempts)
	}
	if hash := fixture.hash(t, "c"); len(hash) != 0 {
		t.Fatalf("unseen quarantine stored a successful source hash: %x", hash)
	}
	if fixture.count(t, drainVectorTable) != 2 {
		t.Fatalf("unseen refused record acquired a vector")
	}
	fixture.embedder.rejectText = ""
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='gamma c' WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "c")
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.status(t, "c") != "ready" || fixture.count(t, drainVectorTable) != 3 {
		t.Fatalf("repaired unseen record did not recover: status=%q vectors=%d", fixture.status(t, "c"), fixture.count(t, drainVectorTable))
	}
}

// TestSemanticStaleRecordsStillRankOnTheirLastGoodVector is the freshness
// tradeoff stated as a test: marking a record stale removes it from neither
// ranking nor similarity, it only stops the vector from being current.
func TestSemanticStaleRecordsStillRankOnTheirLastGoodVector(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	fixture.mark(t, "a")
	fixture.mark(t, "b")
	ranked, err := fixture.manager.Query(ctx, "post", "related", "alpha", textCandidates(`SELECT "id" AS "id" FROM "posts"`), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 2 {
		t.Fatalf("stale records ranked=%d want=2", len(ranked))
	}
	sourceKey, err := semantickey.Encode([]any{"a"})
	if err != nil {
		t.Fatal(err)
	}
	similar, err := fixture.manager.QueryByKey(ctx, "post", "related", sourceKey, textCandidates(`SELECT "id" AS "id" FROM "posts"`), 10)
	if err != nil {
		t.Fatalf("similarity from a stale source: %v", err)
	}
	betaKey, err := semantickey.Encode([]any{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(similar) != 1 || similar[0].Key != betaKey {
		t.Fatalf("similarity from a stale source=%#v", similar)
	}
}

// TestSemanticReconcileFlipsMarkedRecordsAndSeesUnmarkedWrites is the division
// of labour: reconciliation is the only thing that observes a write Golem never
// saw, and it settles a mark that described no change for free.
func TestSemanticReconcileFlipsMarkedRecordsAndSeesUnmarkedWrites(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	fixture.mark(t, "a")
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='beta revised' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if fixture.embedder.calls() != 3 {
		t.Fatalf("reconcile embedded=%d want=3 (only the record that actually changed)", fixture.embedder.calls())
	}
	if got := fixture.embedder.texts()[2]; !strings.Contains(got, "beta revised") {
		t.Fatalf("reconcile embedded %q, want the unmarked write", got)
	}
	if fixture.status(t, "a") != "ready" {
		t.Fatalf("marked but unchanged record status=%q", fixture.status(t, "a"))
	}
}

// TestSemanticDrainBoundsOnePassAndChainsTheRemainder pins the page: one drain
// claims at most semanticStaleProbe records, reports the rest, and the chained
// pass finishes them. It also drives every chunked statement, since the probed
// page is wider than one statement's key binding.
func TestSemanticDrainBoundsOnePassAndChainsTheRemainder(t *testing.T) {
	ctx := context.Background()
	total := semanticStaleProbe + 44
	fixture := newDrainFixture(t)
	if _, err := fixture.db.Exec(`DELETE FROM "posts"`); err != nil {
		t.Fatal(err)
	}
	for ordinal := range total {
		if _, err := fixture.db.Exec(`INSERT INTO "posts" (id,title) VALUES (?,?)`, fmt.Sprintf("k%06d", ordinal), fmt.Sprintf("alpha %d", ordinal)); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	seeded := fixture.embedder.calls()
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='beta '||id`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE "` + drainStateTable + `" SET status='pending'`); err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.manager.Drain(ctx, "post", "related")
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("a page-bounded drain did not report its remainder")
	}
	if got := fixture.embedder.calls() - seeded; got != semanticStaleProbe {
		t.Fatalf("first pass embedded=%d want=%d", got, semanticStaleProbe)
	}
	pending, err = fixture.manager.Drain(ctx, "post", "related")
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("the chained pass left work behind")
	}
	if got := fixture.embedder.calls() - seeded; got != total {
		t.Fatalf("chained pass embedded=%d want=%d", got, total)
	}
	var stale int
	if err := fixture.db.Get(&stale, `SELECT COUNT(*) FROM "`+drainStateTable+`" WHERE status<>'ready'`); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("records left marked=%d", stale)
	}
}

const markStateTable = "_golem_semantic_semantic-part-related_state"
const markVectorTable = "_golem_semantic_semantic-part-related_vec"

type markFixture struct {
	db       *sqlx.DB
	manager  *Manager
	index    Index
	table    physical.PhysicalTable
	embedder *deterministicProvider
}

// newMarkFixture builds a composite primary key that spans the identity kinds
// whose logical and physical representations differ: a UUID mirrored into a
// TEXT column, an integer, and a blob. Composite order is part of the contract,
// so a reordered projection must not produce the same record key.
func newMarkFixture(t *testing.T) markFixture {
	t.Helper()
	database, err := sqlitevec.Open("file:" + t.TempDir() + "/mark.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	db := sqlx.NewDb(database, "sqlite3")
	if _, err := db.Exec(`
CREATE TABLE "parts" ("id" TEXT NOT NULL,"serial" INTEGER NOT NULL,"tag" BLOB NOT NULL,"name" TEXT, PRIMARY KEY ("id","serial","tag")) STRICT;
CREATE TABLE "` + markStateTable + `" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL,"serial" INTEGER NOT NULL,"tag" BLOB NOT NULL,
  CHECK (status IN ('pending','ready','failed'))) STRICT;
CREATE INDEX "_golem_semantic_semantic-part-related_state_stale" ON "` + markStateTable + `" ("record_key" ASC) WHERE "status" <> 'ready';
CREATE VIRTUAL TABLE "` + markVectorTable + `" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine)`); err != nil {
		t.Fatal(err)
	}
	table := physical.PhysicalTable{
		ID: "part", Name: "parts",
		Columns: []physical.PhysicalColumn{
			{ID: "part-id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "part-serial", Name: "serial", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}},
			{ID: "part-tag", Name: "tag", Ordinal: 2, Storage: physical.StorageType{Kind: physical.StorageSQLiteBlob}},
			{ID: "part-name", Name: "name", Ordinal: 3, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "part-primary", Name: "pk_parts", Columns: []ir.FieldID{"part-id", "part-serial", "part-tag"}},
	}
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 8)
	embedder := &deterministicProvider{specification: specification, database: db}
	index := Index{
		Descriptor: semanticstorage.Descriptor{
			ID: "semantic-part-related", ModelID: "part", Name: "related",
			Storage: "_golem_semantic_semantic-part-related",
			Fields:  []ir.FieldID{"part-name"},
			Identity: []semanticstorage.IdentityColumn{
				{Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, NotNull: true},
				{Name: "serial", Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, NotNull: true},
				{Name: "tag", Storage: physical.StorageType{Kind: physical.StorageSQLiteBlob}, NotNull: true},
			},
		},
		Provider: embedder, Specification: specification, SpaceFingerprint: [32]byte{7},
	}
	manager := &Manager{
		database: db, provider: ir.SQLite,
		schema:  physical.PhysicalSchema{Namespace: physical.Namespace{Name: "main"}, Tables: []physical.PhysicalTable{table}},
		indexes: []Index{index},
	}
	return markFixture{db: db, manager: manager, index: index, table: table, embedder: embedder}
}

var markPartUUID = [16]byte{0x10, 0x00, 0x00, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01}

func markPartIdentity() []any { return []any{markPartUUID, int64(7), []byte{0xde, 0xad, 0xbe, 0xef}} }

func markPartKey(t *testing.T) string {
	t.Helper()
	key, err := semantickey.Encode(markPartIdentity())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func (fixture markFixture) insertOwner(t *testing.T, name string) {
	t.Helper()
	if _, err := fixture.db.Exec(`INSERT INTO "parts" ("id","serial","tag","name") VALUES (?,?,?,?)`,
		"10000000-0000-0000-0000-000000000001", int64(7), []byte{0xde, 0xad, 0xbe, 0xef}, name); err != nil {
		t.Fatal(err)
	}
}

// TestSemanticMarkStaleEmbedsABrandNewRecord is the failure the mark must never
// have. The drain reaches owner rows through the mirrored identity columns, so
// a mark bound in the wrong physical representation joins nothing, is
// classified as an absent owner, and has its vector deleted — a write would
// destroy the index entry for the row it just changed, silently. Asserting the
// record was EMBEDDED rejects that and the flip-to-ready hole at once.
func TestSemanticMarkStaleEmbedsABrandNewRecord(t *testing.T) {
	ctx := context.Background()
	fixture := newMarkFixture(t)
	fixture.insertOwner(t, "alpha part")
	key := markPartKey(t)
	if err := fixture.manager.MarkStale(ctx, fixture.db, "part", []MarkRecord{{Key: key, Identity: markPartIdentity()}}); err != nil {
		t.Fatal(err)
	}
	var status string
	var hash []byte
	if err := fixture.db.QueryRow(`SELECT status,source_hash FROM "`+markStateTable+`" WHERE record_key=?`, key).Scan(&status, &hash); err != nil {
		t.Fatalf("mark did not write a state row: %v", err)
	}
	if status != "pending" || len(hash) != 0 {
		t.Fatalf("brand-new mark status=%q source_hash=%d bytes, want pending and empty", status, len(hash))
	}
	if _, err := fixture.manager.Drain(ctx, "part", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.embedder.calls() != 1 {
		t.Fatalf("brand-new marked record embedded=%d want=1 (0 means it was flipped to ready or deleted as an absent owner)", fixture.embedder.calls())
	}
	var states, vectors int
	if err := fixture.db.Get(&states, `SELECT COUNT(*) FROM "`+markStateTable+`" WHERE status='ready'`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Get(&vectors, `SELECT COUNT(*) FROM "`+markVectorTable+`"`); err != nil {
		t.Fatal(err)
	}
	if states != 1 || vectors != 1 {
		t.Fatalf("after drain ready=%d vectors=%d want=1,1", states, vectors)
	}
}

// TestSemanticMarkStaleStoresTheKeyTheRefreshScanProduces is the other half of
// the same invariant: one encoder, one primary-key order, both paths. If these
// ever diverge — a reordered composite key is enough — marks would never match
// their records and nothing would report it.
func TestSemanticMarkStaleStoresTheKeyTheRefreshScanProduces(t *testing.T) {
	ctx := context.Background()
	fixture := newMarkFixture(t)
	fixture.insertOwner(t, "alpha part")
	if err := fixture.manager.MarkStale(ctx, fixture.db, "part", []MarkRecord{{Key: markPartKey(t), Identity: markPartIdentity()}}); err != nil {
		t.Fatal(err)
	}
	var marked string
	if err := fixture.db.Get(&marked, `SELECT record_key FROM "`+markStateTable+`"`); err != nil {
		t.Fatal(err)
	}
	scanned, err := fixture.manager.scanSources(ctx, fixture.table, fixture.index)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) != 1 {
		t.Fatalf("refresh scan rows=%d want=1", len(scanned))
	}
	if marked != scanned[0].key {
		t.Fatalf("mark stored key=%q, refresh scan produces %q", marked, scanned[0].key)
	}
	if err := fixture.manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := fixture.db.Get(&stored, `SELECT COUNT(*) FROM "`+markStateTable+`"`); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("reconcile created a second state row: rows=%d — the mark key and the scan key diverged", stored)
	}
}

// TestSemanticMarkStalePreservesTheHashOfAnIndexedRecord keeps the cheap path
// cheap: a mark on a record that has already been embedded must leave its hash
// and fingerprint alone, or the drain would re-embed every marked row.
func TestSemanticMarkStalePreservesTheHashOfAnIndexedRecord(t *testing.T) {
	ctx := context.Background()
	fixture := newMarkFixture(t)
	fixture.insertOwner(t, "alpha part")
	if err := fixture.manager.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if fixture.embedder.calls() != 1 {
		t.Fatalf("seed embedded=%d want=1", fixture.embedder.calls())
	}
	key := markPartKey(t)
	var before []byte
	if err := fixture.db.Get(&before, `SELECT source_hash FROM "`+markStateTable+`" WHERE record_key=?`, key); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.MarkStale(ctx, fixture.db, "part", []MarkRecord{{Key: key, Identity: markPartIdentity()}}); err != nil {
		t.Fatal(err)
	}
	var after []byte
	var status string
	if err := fixture.db.QueryRow(`SELECT source_hash,status FROM "`+markStateTable+`" WHERE record_key=?`, key).Scan(&after, &status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || !equalBytes(before, after) || len(after) != 32 {
		t.Fatalf("mark on an indexed record status=%q hash preserved=%t", status, equalBytes(before, after))
	}
	if _, err := fixture.manager.Drain(ctx, "part", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.embedder.calls() != 1 {
		t.Fatalf("unchanged marked record was re-embedded: calls=%d want=1", fixture.embedder.calls())
	}
}

func TestSemanticMarkStaleAdvancesDatabaseGeneration(t *testing.T) {
	ctx := context.Background()
	fixture := newMarkFixture(t)
	fixture.insertOwner(t, "alpha part")
	key := markPartKey(t)
	future := time.Now().UTC().Add(time.Hour).UnixMicro()
	if _, err := fixture.db.Exec(`INSERT INTO "`+markStateTable+`" (record_key,source_hash,space_fingerprint,status,attempt_count,error_code,updated_at,"id","serial","tag") VALUES (?,x'',?,'ready',0,NULL,?,?,?,?)`,
		key, fmt.Sprintf("%x", fixture.index.SpaceFingerprint), future, "10000000-0000-0000-0000-000000000001", int64(7), []byte{0xde, 0xad, 0xbe, 0xef}); err != nil {
		t.Fatal(err)
	}
	mark := []MarkRecord{{Key: key, Identity: markPartIdentity()}}
	for want := future + 1; want <= future+2; want++ {
		if err := fixture.manager.MarkStale(ctx, fixture.db, "part", mark); err != nil {
			t.Fatal(err)
		}
		var generation int64
		if err := fixture.db.Get(&generation, `SELECT updated_at FROM "`+markStateTable+`" WHERE record_key=?`, key); err != nil {
			t.Fatal(err)
		}
		if generation != want {
			t.Fatalf("stale generation=%d want=%d", generation, want)
		}
	}
}

// TestSemanticMarkStaleRefusesAKeyThatDisagreesWithItsIdentity keeps a caller
// that computes record keys by a different route from silently marking records
// no drain can find.
func TestSemanticMarkStaleRefusesAKeyThatDisagreesWithItsIdentity(t *testing.T) {
	ctx := context.Background()
	fixture := newMarkFixture(t)
	fixture.insertOwner(t, "alpha part")
	err := fixture.manager.MarkStale(ctx, fixture.db, "part", []MarkRecord{{Key: "golem-semantic-key:v1|4:s:1:x", Identity: markPartIdentity()}})
	if err == nil || !strings.Contains(err.Error(), "does not match its primary identity") {
		t.Fatalf("disagreeing key error=%v", err)
	}
	var rows int
	if err := fixture.db.Get(&rows, `SELECT COUNT(*) FROM "`+markStateTable+`"`); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("a refused mark still wrote %d rows", rows)
	}
}

// TestSemanticMarkIdentityConversionMatchesStoredRepresentation pins the table
// the mark converts by. Each case is the value the owner column actually holds,
// so a conversion that drifts from its provider's codec is caught here rather
// than by a vector quietly disappearing.
func TestSemanticMarkIdentityConversionMatchesStoredRepresentation(t *testing.T) {
	instant := time.Date(2026, 3, 4, 5, 6, 7, 891011567, time.UTC)
	for _, testCase := range []struct {
		name    string
		kind    physical.StorageKind
		value   any
		want    any
		refused bool
	}{
		{name: "sqlite uuid text", kind: physical.StorageSQLiteText, value: markPartUUID, want: "10000000-0000-0000-0000-000000000001"},
		{name: "sqlite text", kind: physical.StorageSQLiteText, value: "t0", want: "t0"},
		{name: "sqlite integer", kind: physical.StorageSQLiteInteger, value: int64(7), want: int64(7)},
		{name: "sqlite blob", kind: physical.StorageSQLiteBlob, value: []byte{1, 2}, want: []byte{1, 2}},
		{name: "sqlite datetime integer", kind: physical.StorageSQLiteInteger, value: instant, want: instant.UnixMicro()},
		{name: "postgres uuid", kind: physical.StoragePostgreSQLUUID, value: markPartUUID, want: "10000000-0000-0000-0000-000000000001"},
		{name: "postgres bigint", kind: physical.StoragePostgreSQLBigInt, value: int64(7), want: int64(7)},
		{name: "postgres integer", kind: physical.StoragePostgreSQLInteger, value: int64(7), want: int32(7)},
		{name: "postgres smallint", kind: physical.StoragePostgreSQLSmallInt, value: int64(7), want: int16(7)},
		{name: "postgres bytea", kind: physical.StoragePostgreSQLBytea, value: []byte{1, 2}, want: []byte{1, 2}},
		{name: "postgres timestamptz", kind: physical.StoragePostgreSQLTimestampTZ, value: instant, want: instant.Truncate(time.Microsecond)},
		{name: "postgres boolean", kind: physical.StoragePostgreSQLBoolean, value: true, want: true},
		{name: "sqlite integer rejects bool", kind: physical.StorageSQLiteInteger, value: true, refused: true},
		{name: "postgres integer overflow", kind: physical.StoragePostgreSQLInteger, value: int64(math.MaxInt32) + 1, refused: true},
		{name: "unconvertible kind", kind: physical.StoragePostgreSQLJSONB, value: "{}", refused: true},
		{name: "unconvertible go type", kind: physical.StorageSQLiteText, value: 3.5, refused: true},
		{name: "null identity", kind: physical.StorageSQLiteText, value: nil, refused: true},
	} {
		manager := &Manager{}
		column := semanticstorage.IdentityColumn{Name: "id", Storage: physical.StorageType{Kind: testCase.kind}, NotNull: true}
		got, err := manager.markIdentityValue(column, testCase.value)
		if testCase.refused {
			if err == nil {
				t.Fatalf("%s: converted %#v to %#v, want a refusal", testCase.name, testCase.value, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if fmt.Sprintf("%T:%v", got, got) != fmt.Sprintf("%T:%v", testCase.want, testCase.want) {
			t.Fatalf("%s: converted to %T:%v want %T:%v", testCase.name, got, got, testCase.want, testCase.want)
		}
	}
}

// TestSemanticMarkStaleRefusesAnIdentityItCannotKeyConsistently covers the
// closed class of identity kinds SQLite cannot round-trip: those whose logical
// encoding differs from the encoding of the value the column actually stores.
// SQLite holds a DateTime as microseconds (i:) and a bool as an integer (i:),
// while the logical values encode as t: and b:, so a key derived from the
// caller's value can never equal the key the refresh scan reads back. Refusing
// the write is the only honest outcome — indexing a row under two different
// keys is not. PostgreSQL has neither problem: timestamptz and boolean both
// scan back as the same Go types they were written from.
func TestSemanticMarkStaleRefusesAnIdentityItCannotKeyConsistently(t *testing.T) {
	instant := time.Date(2026, 3, 4, 5, 6, 7, 891011000, time.UTC)
	manager := &Manager{provider: ir.SQLite}
	index := func(name physical.PhysicalName) Index {
		return Index{Descriptor: semanticstorage.Descriptor{
			ModelID: "event", Name: "related",
			Identity: []semanticstorage.IdentityColumn{{Name: name, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, NotNull: true}},
		}}
	}
	for _, testCase := range []struct {
		name    string
		column  physical.PhysicalName
		logical any
	}{
		{name: "datetime", column: "at", logical: instant},
		{name: "bool", column: "active", logical: true},
	} {
		key, err := semantickey.Encode([]any{testCase.logical})
		if err != nil {
			t.Fatal(err)
		}
		_, err = manager.markRows(index(testCase.column), []MarkRecord{{Key: key, Identity: []any{testCase.logical}}})
		if err == nil {
			t.Fatalf("%s: SQLite identity was accepted under a key the refresh scan cannot produce", testCase.name)
		}
		if !strings.Contains(err.Error(), "does not match its primary identity") && !strings.Contains(err.Error(), "cannot bind") {
			t.Fatalf("%s: error=%v", testCase.name, err)
		}
	}
	stored, err := semantickey.Encode([]any{instant.UnixMicro()})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := manager.markRows(index("at"), []MarkRecord{{Key: stored, Identity: []any{instant}}})
	if err != nil || len(rows) != 1 || rows[0].key != stored {
		t.Fatalf("stored-representation key rows=%#v error=%v", rows, err)
	}
}

// TestSemanticMarkStaleOrdersAndDeduplicatesRows keeps concurrent writers
// taking shadow-row locks in one order, and keeps a batch that touches the same
// record twice from conflicting with itself inside a single statement.
func TestSemanticMarkStaleOrdersAndDeduplicatesRows(t *testing.T) {
	manager := &Manager{provider: ir.SQLite}
	index := Index{Descriptor: semanticstorage.Descriptor{
		ModelID: "part", Name: "related",
		Identity: []semanticstorage.IdentityColumn{{Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, NotNull: true}},
	}}
	records := make([]MarkRecord, 0, 4)
	for _, id := range []string{"c", "a", "b", "a"} {
		key, err := semantickey.Encode([]any{id})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, MarkRecord{Key: key, Identity: []any{id}})
	}
	rows, err := manager.markRows(index, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d want=3 deduplicated", len(rows))
	}
	for position := 1; position < len(rows); position++ {
		if rows[position-1].key >= rows[position].key {
			t.Fatalf("rows are not ordered by record key: %#v", rows)
		}
	}
}

func TestSemanticSimilarityDistinguishesTheThreeSourceVectorStates(t *testing.T) {
	ctx := context.Background()
	candidates := textCandidates(`SELECT "id" AS "id" FROM "posts"`)
	sourceKey, err := semantickey.Encode([]any{"a"})
	if err != nil {
		t.Fatal(err)
	}
	absentKey, err := semantickey.Encode([]any{"never-embedded"})
	if err != nil {
		t.Fatal(err)
	}

	absent := newDrainFixture(t)
	_, absentErr := absent.manager.QueryByKey(ctx, "post", "related", absentKey, candidates, 10)
	if absentErr == nil || absentErr.Error() != "P9_SEMANTIC_QUERY: semantic source vector is unavailable" {
		t.Fatalf("never embedded source: %v", absentErr)
	}

	broken := newDrainFixture(t)
	if _, err := broken.db.Exec(`DROP TABLE "` + drainVectorTable + `"`); err != nil {
		t.Fatal(err)
	}
	_, brokenErr := broken.manager.QueryByKey(ctx, "post", "related", sourceKey, candidates, 10)
	if brokenErr == nil || brokenErr.Error() != "P9_SEMANTIC_QUERY: semantic source vector read failed" {
		t.Fatalf("storage read failure: %v", brokenErr)
	}
	if brokenErr.Error() == absentErr.Error() {
		t.Fatal("a storage read failure reports as if the source was never embedded")
	}

	empty := newDrainFixture(t)
	if _, err := empty.db.Exec(`DROP TABLE "` + drainVectorTable + `";
CREATE TABLE "` + drainVectorTable + `" (record_key TEXT NOT NULL PRIMARY KEY,embedding BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := empty.db.Exec(`INSERT INTO "`+drainVectorTable+`" (record_key,embedding) VALUES (?,X'')`, sourceKey); err != nil {
		t.Fatal(err)
	}
	_, emptyErr := empty.manager.QueryByKey(ctx, "post", "related", sourceKey, candidates, 10)
	if emptyErr == nil || emptyErr.Error() != "P9_SEMANTIC_QUERY: semantic source vector is empty" {
		t.Fatalf("empty stored vector: %v", emptyErr)
	}
}

func TestSemanticSimilaritySourceVectorFailureDisclosesNothing(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	sourceKey, err := semantickey.Encode([]any{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`DROP TABLE "` + drainVectorTable + `"`); err != nil {
		t.Fatal(err)
	}
	_, failure := fixture.manager.QueryByKey(ctx, "post", "related", sourceKey, textCandidates(`SELECT "id" AS "id" FROM "posts"`), 10)
	if failure == nil {
		t.Fatal("a dropped vector table did not fail")
	}
	for _, leak := range []string{sourceKey, drainVectorTable, "SELECT", "embedding", "no such table", "sqlite"} {
		if strings.Contains(failure.Error(), leak) {
			t.Fatalf("source vector failure %q discloses %q", failure.Error(), leak)
		}
	}
	if errors.Unwrap(failure) != nil {
		t.Fatalf("source vector failure carries an unwrappable cause: %v", errors.Unwrap(failure))
	}
}

func TestSemanticSimilaritySourceVectorPreservesSafeCancellationCauses(t *testing.T) {
	cancelledCause := fmt.Errorf("driver password: %w", context.Canceled)
	_, cancelled := classifySourceVector("", cancelledCause)
	if !errors.Is(cancelled, context.Canceled) || errors.Is(cancelled, context.DeadlineExceeded) || strings.Contains(cancelled.Error(), "password") {
		t.Fatalf("cancelled read classification=%v", cancelled)
	}
	timedOutCause := fmt.Errorf("private statement: %w", context.DeadlineExceeded)
	_, timedOut := classifySourceVector([]byte(nil), timedOutCause)
	if !errors.Is(timedOut, context.DeadlineExceeded) || errors.Is(timedOut, context.Canceled) || strings.Contains(timedOut.Error(), "private statement") {
		t.Fatalf("timed-out read classification=%v", timedOut)
	}
	private := errors.New("driver password and statement")
	_, sealed := classifySourceVector("", private)
	if errors.Unwrap(sealed) != nil || strings.Contains(sealed.Error(), private.Error()) {
		t.Fatalf("driver failure was not sealed: %v", sealed)
	}
}
