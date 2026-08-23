package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
)

// semanticMarkEmbedder is the deterministic embedding provider every semantic
// fixture uses. It counts calls so a gate can tell an embedded record from one
// the drain flipped back to ready without reaching a provider at all.
type semanticMarkEmbedder struct {
	specification embedding.Specification
	mu            sync.Mutex
	calls         int
}

func (provider *semanticMarkEmbedder) Specification() embedding.Specification {
	return provider.specification
}

func (provider *semanticMarkEmbedder) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls += len(inputs)
	result := make([]embedding.Vector, len(inputs))
	for index := range inputs {
		result[index], _ = embedding.NewVector([]float32{1, 0, 0})
	}
	return result, nil
}

func (provider *semanticMarkEmbedder) count() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

// configureSemanticApp supplies what a model declaring a semantic index now
// requires at Open: an embedding provider for its declared space, and the
// durable queue its drain jobs are recorded on.
func configureSemanticApp(t testing.TB, embedder *semanticMarkEmbedder) func(*Config[mutationResultPrincipal, mutationResultActor]) {
	t.Helper()
	specification, err := embedding.NewSpecification("test", "model", "v1", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	embedder.specification = specification
	registry, err := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	if err != nil {
		t.Fatal(err)
	}
	return func(config *Config[mutationResultPrincipal, mutationResultActor]) {
		config.Embeddings = registry
		config.Queue = &QueueConfig{Registry: queue.NewRegistry()}
	}
}

func newSemanticMarkFixture(t *testing.T) mutationResultFixture {
	t.Helper()
	fixture, _ := newSemanticMarkFixtureWithEmbedder(t, MutationLimits{})
	return fixture
}

func newSemanticMarkFixtureWithEmbedder(t *testing.T, limits MutationLimits) (mutationResultFixture, *semanticMarkEmbedder) {
	t.Helper()
	embedder := &semanticMarkEmbedder{}
	fixture := openConfiguredMutationResultFixture(t, schematest.NewSemanticIndexed(t), limits, nil, nil, nil, true, configureSemanticApp(t, embedder))
	return fixture, embedder
}

func newSemanticVersionedFixture(t *testing.T) mutationVocabularyFixture {
	t.Helper()
	ctx := context.Background()
	schema := schematest.WithSemanticIndex(t, schematest.NewOptimisticConcurrency(t))
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "semantic-versioned.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
		t.Fatal(err)
	}
	fixture := openMutationVocabularyFixtureConfigured(t, database, golem.SQLite, schema, configureSemanticApp(t, &semanticMarkEmbedder{}))
	user := golem.GeneratedCreateInput[mutationResultUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "alice"),
	)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, user); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// semanticMarkKeys reads the transaction's mark buffer. It is deliberately
// separate from semanticFactCount: a shared assertion would let a missing mark
// hide behind a present outbox row.
func semanticMarkKeys(t testing.TB, binding *executionBinding) []string {
	t.Helper()
	state, err := binding.mutationState()
	if err != nil {
		t.Fatal(err)
	}
	marks := state.semanticMarks()
	keys := make([]string, len(marks))
	for index, mark := range marks {
		keys[index] = mark.key
	}
	sort.Strings(keys)
	return keys
}

func semanticFactCount(t testing.TB, binding *executionBinding) int {
	t.Helper()
	state, err := binding.mutationState()
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.facts)
}

func semanticPostKey(t testing.TB, id byte) string {
	t.Helper()
	value := golem.UUID{15: id}
	return "golem-semantic-key:v1|37:uuid:" + hex.EncodeToString(value[:])
}

// TestSemanticMarkAtEveryMutationOperationKind is the per-operation-kind kernel
// gate. mutationir.Operation is a closed enum, so create/update/delete through
// the scalar kernel plus update-many/delete-many through the batch consumers is
// complete coverage of the write shapes a root mutation can take.
func TestSemanticMarkAtEveryMutationOperationKind(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation mutationir.Operation
		seed      []byte
		run       func(context.Context, testing.TB, mutationResultFixture, *CallerTx[mutationResultPrincipal, mutationResultActor]) error
		wantKeys  []byte
		wantFacts int
	}{
		{
			name: "create", operation: mutationir.Create, wantKeys: []byte{31}, wantFacts: 1,
			run: func(ctx context.Context, t testing.TB, fixture mutationResultFixture, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(31, golem.UUID{15: 1}, "marked"))
				return err
			},
		},
		{
			name: "update", operation: mutationir.Update, seed: []byte{32}, wantKeys: []byte{32}, wantFacts: 1,
			run: func(ctx context.Context, t testing.TB, fixture mutationResultFixture, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpdate(ctx, transaction, fixture.postDescriptor, fixture.target(32), fixture.updateTitle("marked"))
				return err
			},
		},
		{
			name: "delete", operation: mutationir.Delete, seed: []byte{33}, wantKeys: []byte{33}, wantFacts: 1,
			run: func(ctx context.Context, t testing.TB, fixture mutationResultFixture, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxDelete(ctx, transaction, fixture.postDescriptor, fixture.target(33))
				return err
			},
		},
		{
			name: "update-many", operation: mutationir.UpdateMany, seed: []byte{34, 35}, wantKeys: []byte{34, 35}, wantFacts: 2,
			run: func(ctx context.Context, t testing.TB, fixture mutationResultFixture, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpdateMany(ctx, transaction, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 34}, golem.UUID{15: 35}), fixture.updateManyTitle("marked"))
				return err
			},
		},
		{
			name: "delete-many", operation: mutationir.DeleteMany, seed: []byte{36, 37}, wantKeys: []byte{36, 37}, wantFacts: 2,
			run: func(ctx context.Context, t testing.TB, fixture mutationResultFixture, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxDeleteMany(ctx, transaction, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 36}, golem.UUID{15: 37}))
				return err
			},
		},
		{
			name: "upsert", operation: mutationir.Upsert, wantKeys: []byte{38}, wantFacts: 1,
			run: func(ctx context.Context, t testing.TB, fixture mutationResultFixture, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpsert(ctx, transaction, fixture.postDescriptor, fixture.target(38), fixture.createPost(38, golem.UUID{15: 1}, "marked"), fixture.updateTitle("marked"))
				return err
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newSemanticMarkFixture(t)
			for _, id := range test.seed {
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "seeded")); err != nil {
					t.Fatal(err)
				}
			}
			caller := mustMutationResultCaller(t, fixture)
			var marks []string
			var facts int
			if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				if err := test.run(ctx, t, fixture, transaction); err != nil {
					return err
				}
				marks = semanticMarkKeys(t, transaction.caller.executor)
				facts = semanticFactCount(t, transaction.caller.executor)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			assertSemanticMarkKeys(t, marks, test.wantKeys)
			if facts != test.wantFacts {
				t.Fatalf("%s outbox facts=%d want=%d", test.name, facts, test.wantFacts)
			}
		})
	}
}

// TestSemanticMarkOnVersionedWrites is the reason the scalar kernel tail had to
// be extracted. Every optimistic-concurrency path runs the second executor, so
// without one shared owner these writes would silently skip marking.
func TestSemanticMarkOnVersionedWrites(t *testing.T) {
	ctx := context.Background()
	fixture := newSemanticVersionedFixture(t)
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	decimal, err := golem.ParseDecimal("1.25")
	if err != nil {
		t.Fatal(err)
	}
	decimalField := golem.GeneratedEqualField[mutationResultPost, golem.Decimal](fixture.schema.PostDecimal)
	create := func(id byte, title string) golem.CreateInput[mutationResultPost] {
		return golem.GeneratedCreateInput(fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, title),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, decimalField, decimal),
		)
	}
	for _, id := range []byte{41, 42} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, create(id, "seeded")); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name     string
		run      func(context.Context, *CallerTx[mutationResultPrincipal, mutationResultActor]) error
		wantKeys []byte
	}{
		{
			name: "update-versioned", wantKeys: []byte{41},
			run: func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpdateVersioned(ctx, transaction, fixture.postDescriptor, fixture.target(41), golem.ExpectVersion(1), fixture.updateTitle("versioned"))
				return err
			},
		},
		{
			name: "delete-versioned", wantKeys: []byte{42},
			run: func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxDeleteVersioned(ctx, transaction, fixture.postDescriptor, fixture.target(42), golem.ExpectVersion(1))
				return err
			},
		},
		{
			name: "upsert-versioned-absent", wantKeys: []byte{43},
			run: func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpsertVersioned(ctx, transaction, fixture.postDescriptor, fixture.target(43), golem.ExpectAbsent(), create(43, "versioned"), fixture.updateTitle("unused"))
				return err
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var marks []string
			var facts int
			if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				if err := test.run(ctx, transaction); err != nil {
					return err
				}
				marks = semanticMarkKeys(t, transaction.caller.executor)
				facts = semanticFactCount(t, transaction.caller.executor)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			assertSemanticMarkKeys(t, marks, test.wantKeys)
			if facts != 1 {
				t.Fatalf("%s outbox facts=%d want=1", test.name, facts)
			}
		})
	}
}

// TestSemanticMarkIsAbsentWithoutAnIndexAndAfterRollback pins both negative
// halves: a model with no semantic index is never marked, and a rolled-back
// scope leaves no mark behind for the surrounding transaction to flush.
func TestSemanticMarkIsAbsentWithoutAnIndexAndAfterRollback(t *testing.T) {
	ctx := context.Background()
	plain := openMutationResultFixture(t, schematest.NewSubscribedIndexed(t), MutationLimits{}, nil, nil, nil, true)
	plainCaller := mustMutationResultCaller(t, plain)
	var plainMarks []string
	var plainFacts int
	if err := CallerTransaction(ctx, plainCaller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, err := CallerTxCreate(ctx, transaction, plain.postDescriptor, plain.createPost(51, golem.UUID{15: 1}, "unmarked")); err != nil {
			return err
		}
		plainMarks = semanticMarkKeys(t, transaction.caller.executor)
		plainFacts = semanticFactCount(t, transaction.caller.executor)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(plainMarks) != 0 {
		t.Fatalf("model without a semantic index was marked: %v", plainMarks)
	}
	if plainFacts != 1 {
		t.Fatalf("unindexed subscribed create facts=%d want=1", plainFacts)
	}

	fixture, _ := newSemanticMarkFixtureWithEmbedder(t, MutationLimits{MaxTouchedRows: 1})
	caller := mustMutationResultCaller(t, fixture)
	for _, id := range []byte{52, 53} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "seeded")); err != nil {
			t.Fatal(err)
		}
	}
	var afterRollback []string
	var factsAfterRollback int
	if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, err := CallerTxUpdate(ctx, transaction, fixture.postDescriptor, fixture.target(52), fixture.updateTitle("kept")); err != nil {
			return err
		}
		// The batch scope rolls back on the touched-row ceiling below, which is
		// what must take its marks with it.
		_, batchErr := CallerTxUpdateMany(ctx, transaction, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 53}), fixture.updateManyTitle("rolled-back"))
		if batchErr == nil {
			t.Fatal("batch scope was expected to fail")
		}
		afterRollback = semanticMarkKeys(t, transaction.caller.executor)
		factsAfterRollback = semanticFactCount(t, transaction.caller.executor)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertSemanticMarkKeys(t, afterRollback, []byte{52})
	if factsAfterRollback != 1 {
		t.Fatalf("rolled-back batch left facts=%d want=1", factsAfterRollback)
	}
}

func assertSemanticMarkKeys(t testing.TB, got []string, ids []byte) {
	t.Helper()
	want := make([]string, len(ids))
	for index, id := range ids {
		want[index] = semanticPostKey(t, id)
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("marks=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("marks=%v want=%v", got, want)
		}
	}
}

func semanticShadowTables(t testing.TB, fixture mutationResultFixture) (string, string) {
	t.Helper()
	extensions := fixture.schema.SQLite.Extensions
	if len(extensions) != 1 {
		t.Fatalf("semantic fixture has %d physical extensions", len(extensions))
	}
	storage := "_golem_semantic_" + string(extensions[0].ID)
	return `"` + storage + `_state"`, `"` + storage + `_vec"`
}

func semanticStateRow(t testing.TB, fixture mutationResultFixture, key string) (string, int, bool) {
	t.Helper()
	var status string
	var hashLength int
	query := fixture.app.database.Rebind(`SELECT "status", length("source_hash") FROM ` + mustSemanticStateTable(t, fixture) + ` WHERE "record_key" = ?`)
	row := fixture.app.database.QueryRowxContext(context.Background(), query, key)
	if err := row.Scan(&status, &hashLength); err != nil {
		return "", 0, false
	}
	return status, hashLength, true
}

func mustSemanticStateTable(t testing.TB, fixture mutationResultFixture) string {
	t.Helper()
	state, _ := semanticShadowTables(t, fixture)
	return state
}

func semanticVectorCount(t testing.TB, fixture mutationResultFixture, key string) int {
	t.Helper()
	_, vectors := semanticShadowTables(t, fixture)
	var count int
	query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + vectors + ` WHERE "record_key" = ?`)
	if err := fixture.app.database.GetContext(context.Background(), &count, query, key); err != nil {
		t.Fatal(err)
	}
	return count
}

func semanticDrainJobCount(t testing.TB, fixture mutationResultFixture) int {
	t.Helper()
	var count int
	query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM "golem_queue" WHERE "type" = ?`)
	if err := fixture.app.database.GetContext(context.Background(), &count, query, semanticDrainJobType); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestSemanticMarkOfANewRecordIsDrainedIntoAVector is the end-to-end gate on the
// flush. It proves the three things a mark must do for a record that has never
// been indexed: the shadow row is written inside the write's own transaction,
// its source hash is empty so no real document hash can equal it, and the drain
// therefore embeds the record instead of flipping it to ready for free.
func TestSemanticMarkOfANewRecordIsDrainedIntoAVector(t *testing.T) {
	ctx := context.Background()
	fixture, embedder := newSemanticMarkFixtureWithEmbedder(t, MutationLimits{})
	caller := mustMutationResultCaller(t, fixture)
	// The queue is quiesced first. A drain job left over from an earlier write
	// would do this gate's work for it, and the gate would then survive having
	// its own enqueue deleted.
	if jobs := semanticDrainJobCount(t, fixture); jobs != 0 {
		t.Fatalf("fixture started with %d drain jobs already enqueued", jobs)
	}
	if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(61, golem.UUID{15: 1}, "alpha document")); err != nil {
		t.Fatal(err)
	}
	key := semanticPostKey(t, 61)

	status, hashLength, present := semanticStateRow(t, fixture, key)
	if !present {
		t.Fatal("create wrote no semantic shadow row")
	}
	if status == "ready" {
		t.Fatalf("a never-indexed record was marked %q", status)
	}
	if hashLength != 0 {
		t.Fatalf("insert arm wrote a %d-byte source hash; a new record must carry none", hashLength)
	}
	if jobs := semanticDrainJobCount(t, fixture); jobs != 1 {
		t.Fatalf("drain jobs=%d want=1", jobs)
	}
	if embedder.count() != 0 {
		t.Fatalf("marking reached the embedding provider %d times", embedder.count())
	}

	if _, err := fixture.app.semantic.Drain(ctx, semanticIndexModel(fixture.schema.Post), schematest.SemanticIndexName); err != nil {
		t.Fatal(err)
	}
	if embedder.count() != 1 {
		t.Fatalf("drain embedded %d records; the marked record was flipped to ready without a vector", embedder.count())
	}
	if vectors := semanticVectorCount(t, fixture, key); vectors != 1 {
		t.Fatalf("vectors=%d want=1", vectors)
	}
	if status, _, _ := semanticStateRow(t, fixture, key); status != "ready" {
		t.Fatalf("drained record status=%q", status)
	}
}

// TestRolledBackWriteLeavesNoSemanticShadowRowOrDrainJob is the negative half:
// the marks, the shadow rows and the drain job are written on the mutation's own
// executor, so a rollback takes all three with it.
func TestRolledBackWriteLeavesNoSemanticShadowRowOrDrainJob(t *testing.T) {
	ctx := context.Background()
	fixture, embedder := newSemanticMarkFixtureWithEmbedder(t, MutationLimits{})
	caller := mustMutationResultCaller(t, fixture)
	sentinel := errors.New("abandon the semantic write")
	err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, createErr := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(62, golem.UUID{15: 1}, "rolled back")); createErr != nil {
			return createErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback err=%v", err)
	}
	if _, _, present := semanticStateRow(t, fixture, semanticPostKey(t, 62)); present {
		t.Fatal("a rolled-back write left a semantic shadow row")
	}
	if jobs := semanticDrainJobCount(t, fixture); jobs != 0 {
		t.Fatalf("a rolled-back write left %d drain jobs", jobs)
	}
	if embedder.count() != 0 {
		t.Fatalf("rolled-back write reached the embedding provider %d times", embedder.count())
	}
}

// TestMarkWithoutAPhysicalIndexRefusesRatherThanDroppingTheMark pins the one
// disagreement the two halves of the decision can have. The mark is taken from
// the compiler's logical model; the shadow write is carried out against the
// physical index inventory. A model that claims the first without the second
// would have every mark silently discarded, so the write fails instead.
func TestMarkWithoutAPhysicalIndexRefusesRatherThanDroppingTheMark(t *testing.T) {
	ctx := context.Background()
	// This fixture declares the index in the model IR only: its provider schema
	// documents carry no semantic extension, so the manager's inventory is empty.
	schema := schematest.NewSemanticIndexed(t)
	schema.SQLite.Extensions, schema.PostgreSQL.Extensions = nil, nil
	schema.Bundle = golem.GeneratedSchemaBundle(
		schema.Bundle.GenerationDigest(), schema.Bundle.GeneratorVersion(), schema.Bundle.TemplateABIVersion(),
		schema.Bundle.Model(), schema.Bundle.Contract(),
		schematest.ProviderDocument(t, golem.SQLite, schema.SQLite), schematest.ProviderDocument(t, golem.PostgreSQL, schema.PostgreSQL),
	)
	fixture := openMutationResultFixture(t, schema, MutationLimits{}, nil, nil, nil, true)
	caller := mustMutationResultCaller(t, fixture)
	_, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(71, golem.UUID{15: 1}, "orphan"))
	if err == nil {
		t.Fatal("a mark with no physical index was accepted")
	}
	if !strings.Contains(err.Error(), "owns no physical index") && !strings.Contains(errorChainText(err), "owns no physical index") {
		t.Fatalf("refusal=%v", err)
	}
	assertMutationResultTitleCount(t, fixture, "orphan", 0)
}

func errorChainText(err error) string {
	var builder strings.Builder
	for current := err; current != nil; current = errors.Unwrap(current) {
		builder.WriteString(current.Error())
		builder.WriteString(" | ")
	}
	return builder.String()
}

// TestSemanticMarkUsesThePrimaryKeyNotTheVerifiedIdentityFields covers the one
// shape where the two differ. Writing a field that belongs to any identity makes
// the plan's behavior IdentityMayChange, and the renderer then widens the
// program's identity verification to the primary key plus every unique key's
// fields. A mark built from that widened list carries more values than the
// shadow contract's mirrored identity and cannot be written at all.
func TestSemanticMarkUsesThePrimaryKeyNotTheVerifiedIdentityFields(t *testing.T) {
	ctx := context.Background()
	embedder := &semanticMarkEmbedder{}
	fixture := openConfiguredMutationResultFixture(t, schematest.NewSemanticIndexedUniqueTitle(t), MutationLimits{}, nil, nil, nil, true, configureSemanticApp(t, embedder))
	caller := mustMutationResultCaller(t, fixture)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(81, golem.UUID{15: 1}, "unique original")); err != nil {
		t.Fatal(err)
	}
	// Title is both the indexed field and a unique key, so this update is the
	// IdentityMayChange case.
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(81), fixture.updateTitle("unique rewritten")); err != nil {
		t.Fatalf("update on a unique indexed field: %v", err)
	}
	status, hashLength, present := semanticStateRow(t, fixture, semanticPostKey(t, 81))
	if !present {
		t.Fatal("identity-changing update wrote no semantic shadow row")
	}
	if status == "ready" || hashLength != 0 {
		t.Fatalf("shadow row status=%q hash=%d bytes", status, hashLength)
	}
}
