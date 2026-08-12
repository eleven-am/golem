package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
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

func TestManagerRedactsEmbeddingProviderErrorCause(t *testing.T) {
	specification, _ := embedding.NewSpecification("test", "private-model", "v1", 3, 8)
	_, privateErr := callEmbeddingProvider(context.Background(), secretFailingProvider{specification: specification}, []embedding.Input{{}})
	publicErr := embedding.NewError(embedding.CodeProvider, privateErr)
	if privateErr == nil || strings.Contains(publicErr.Error(), "canary") || strings.Contains(fmt.Sprint(errors.Unwrap(publicErr)), "canary") {
		t.Fatalf("provider error crossed private boundary: %v cause=%v", publicErr, errors.Unwrap(publicErr))
	}
}

func TestSemanticObservationAndErrorRedactProviderCanary(t *testing.T) {
	specification, _ := embedding.NewSpecification("test", "private-model", "v1", 3, 8)
	collector := &semanticObservationCollector{}
	manager := &Manager{
		provider: ir.SQLite,
		observer: collector,
		indexes: []Index{{
			Descriptor:    semanticstorage.Descriptor{ModelID: "post", Name: "related"},
			Provider:      secretFailingProvider{specification: specification},
			Specification: specification,
		}},
	}
	_, err := manager.Query(context.Background(), "post", "related", "query canary", []string{"private-record-key"}, 1)
	if code, ok := embedding.CodeOf(err); !ok || code != embedding.CodeProvider || strings.Contains(err.Error(), "canary") || strings.Contains(fmt.Sprint(errors.Unwrap(err)), "canary") {
		t.Fatalf("public provider failure was not closed: error=%v cause=%v", err, errors.Unwrap(err))
	}
	assertSemanticObservations(t, collector.take(), []semanticObservation{
		{kind: observe.KindSemantic, operation: observe.OperationSemanticProvider, outcome: observe.OutcomeFailure, reason: observe.ReasonProvider, statements: 0, aggregate: 1},
		{kind: observe.KindSemantic, operation: observe.OperationSemanticRank, outcome: observe.OutcomeFailure, reason: observe.ReasonProvider, statements: 0, aggregate: 1},
	})
}
func (provider *deterministicProvider) calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return len(provider.inputs)
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
CREATE TABLE "_golem_semantic_semantic-post-related_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL) STRICT;
CREATE VIRTUAL TABLE "_golem_semantic_semantic-post-related_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine);
CREATE TABLE "_golem_semantic_semantic-other-unrelated_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL) STRICT;
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
				Descriptor: semanticstorage.Descriptor{ModelID: "post", Name: "related", Storage: "_golem_semantic_semantic-post-related", Fields: []ir.FieldID{"title"}},
				Provider:   selected, Specification: specification,
			},
			{
				Descriptor: semanticstorage.Descriptor{ModelID: "other", Name: "unrelated", Storage: "_golem_semantic_semantic-other-unrelated", Fields: []ir.FieldID{"other-title"}},
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
	if _, err := db.Exec(`CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT); CREATE TABLE "_golem_semantic_semantic-post-related_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL) STRICT; CREATE VIRTUAL TABLE "_golem_semantic_semantic-post-related_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine)`); err != nil {
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
	ranked, err := manager.Query(ctx, "post", "related", "alpha", []string{betaKey}, 1)
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
	if _, err := db.Exec(`CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT); CREATE TABLE "_golem_semantic_semantic-post-related_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL) STRICT; CREATE VIRTUAL TABLE "_golem_semantic_semantic-post-related_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine); INSERT INTO "posts" (id,title) VALUES ('a','alpha'),('b','beta')`); err != nil {
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

	betaKey, _ := semantickey.Encode([]any{"b"})
	if _, err := manager.Query(ctx, "post", "related", "alpha", []string{betaKey}, 1); err != nil {
		t.Fatal(err)
	}
	assertSemanticObservations(t, collector.take(), []semanticObservation{
		{kind: observe.KindSemantic, operation: observe.OperationSemanticProvider, outcome: observe.OutcomeSuccess, reason: observe.ReasonNone, statements: 0, aggregate: 1},
		{kind: observe.KindSemantic, operation: observe.OperationSemanticRank, outcome: observe.OutcomeSuccess, reason: observe.ReasonNone, statements: 1, aggregate: 1},
	})
}

func assertSemanticObservations(t *testing.T, got, want []semanticObservation) {
	t.Helper()
	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("semantic observations=%#v want=%#v", got, want)
	}
}
