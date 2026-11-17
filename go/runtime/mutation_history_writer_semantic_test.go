package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

type mutationHistorySemanticEmbedder struct {
	spec  embedding.Specification
	calls atomic.Int64
}

func (provider *mutationHistorySemanticEmbedder) Specification() embedding.Specification {
	return provider.spec
}

func (provider *mutationHistorySemanticEmbedder) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	provider.calls.Add(int64(len(inputs)))
	result := make([]embedding.Vector, len(inputs))
	for index := range result {
		vector, err := embedding.NewVector([]float32{1, 0, 0})
		if err != nil {
			return nil, err
		}
		result[index] = vector
	}
	return result, nil
}

func TestMutationHistoryWriterSemanticDrainFailureRollsBackSealedCausation(t *testing.T) {
	requireMutationHistoryWriter(t)
	specification, err := embedding.NewSpecification("test", "model", "v1", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &mutationHistorySemanticEmbedder{spec: specification}
	embeddings, err := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	if err != nil {
		t.Fatal(err)
	}
	var committed atomic.Int64
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true,
		postMutationGrants: true, semanticEmbeddings: &embeddings,
		afterCommitError: func(_ context.Context, failure golem.AfterCommitFailure) {
			t.Errorf("unexpected after-commit failure: %v", failure)
		},
		schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture {
			graph := mutationHistoryFixture(t)
			// Reuse the existing semantic schema owner; only adapt its fixture
			// carrier, preserving all graph models and static capture templates.
			semantic := schematest.WithSemanticIndex(t, schematest.Fixture{
				Bundle: graph.Bundle, Registry: graph.Registry, SQLite: graph.SQLite, PostgreSQL: graph.PostgreSQL,
				Post: graph.Post, PostTitle: graph.PostTitle,
			})
			graph.Bundle, graph.Registry, graph.SQLite, graph.PostgreSQL = semantic.Bundle, semantic.Registry, semantic.SQLite, semantic.PostgreSQL
			return graph
		},
		hooks: func(graph schematest.GraphFixture) []golem.HookBinding[policyFactRuntimeActor] {
			return []golem.HookBinding[policyFactRuntimeActor]{
				golem.GeneratedAfterCommitHookBinding[policyFactRuntimeActor, policyFactRuntimePost, golem.UpdateHookResult[policyFactRuntimePost]](graph.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[policyFactRuntimePost]) error {
					committed.Add(1)
					return nil
				}),
			}
		},
	})
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
	field := func(id golem.FieldID) string {
		t.Helper()
		value, present := fixture.app.registry.PhysicalField(golem.SQLite, fixture.schema.Post, id)
		if !present {
			t.Fatal("Post physical field is absent")
		}
		return quote(string(value.ColumnName()))
	}
	versionField, present := fixture.app.registry.PhysicalField(golem.SQLite, fixture.schema.Post, fixture.schema.PostRowVersion)
	if !present {
		t.Fatal("Post version physical metadata is absent")
	}
	posts := quote(string(versionField.TableName()))
	version := field(fixture.schema.PostRowVersion)
	clock := quote(string(physical.RowVersionObject))
	if len(fixture.schema.SQLite.Extensions) != 1 {
		t.Fatal("fixture needs exactly one real semantic index")
	}
	storage, err := semanticstorage.Decode(fixture.schema.SQLite.Extensions[0])
	if err != nil {
		t.Fatal(err)
	}
	shadow := quote(string(storage.Storage) + "_state")
	vectors := quote(string(storage.Storage) + "_vec")
	// Prepare an ordinary preexisting source row. Let the physical schema's
	// default initialize its version, so the provider clock remains coherent.
	seed := "INSERT INTO " + posts + " (" + field(fixture.schema.PostID) + "," + field(fixture.schema.AuthorID) + "," + field(fixture.schema.PostTitle) + ") VALUES (?,?,?)"
	if _, err := fixture.app.database.ExecContext(fixture.ctx, seed, (golem.UUID{15: 252}).String(), (golem.UUID{15: 1}).String(), "before"); err != nil {
		t.Fatal(err)
	}
	readVersion := func() (string, int64, int64) {
		t.Helper()
		var title string
		var rowVersion, clockVersion int64
		query := "SELECT " + field(fixture.schema.PostTitle) + "," + version + " FROM " + posts + " WHERE " + field(fixture.schema.PostID) + "=?"
		if err := fixture.app.database.QueryRowxContext(fixture.ctx, query, (golem.UUID{15: 252}).String()).Scan(&title, &rowVersion); err != nil {
			t.Fatal(err)
		}
		if err := fixture.app.database.GetContext(fixture.ctx, &clockVersion, "SELECT value FROM "+clock); err != nil {
			t.Fatal(err)
		}
		return title, rowVersion, clockVersion
	}
	beforeTitle, beforeVersion, beforeClock := readVersion()
	assertEmpty := func() {
		t.Helper()
		for _, table := range []string{`"_golem_outbox"`, `"_golem_outbox_delivery"`, `"golem_queue"`, shadow, vectors} {
			var count int
			if err := fixture.app.database.GetContext(fixture.ctx, &count, "SELECT COUNT(*) FROM "+table); err != nil || count != 0 {
				t.Fatalf("rollback/preparation left rows in %s: count=%d err=%v", table, count, err)
			}
		}
	}
	assertEmpty()
	// This trigger runs on the actual queue insertion, after normal outbox,
	// delivery and semantic shadow writes. Distinct failures expose a changed
	// flush order instead of letting any earlier SQL error satisfy the test.
	trigger := fmt.Sprintf(`CREATE TRIGGER "reject_history_semantic_drain" BEFORE INSERT ON "golem_queue"
WHEN NEW.type='semantic.drain'
BEGIN
 SELECT CASE WHEN (SELECT COUNT(*) FROM "_golem_outbox" WHERE fact_version=4)!=1 THEN RAISE(ABORT,'writer-order-outbox') END;
 SELECT CASE WHEN (SELECT COUNT(*) FROM "_golem_outbox_delivery")!=1 THEN RAISE(ABORT,'writer-order-delivery') END;
 SELECT CASE WHEN (SELECT COUNT(*) FROM %s WHERE status='pending')!=1 THEN RAISE(ABORT,'writer-order-shadow') END;
 SELECT CASE WHEN (SELECT %s FROM %s)<=%d THEN RAISE(ABORT,'writer-order-version') END;
 SELECT RAISE(ABORT,'writer-semantic-drain-refusal');
END`, shadow, version, posts, beforeVersion)
	if _, err := fixture.app.database.ExecContext(fixture.ctx, trigger); err != nil {
		t.Fatal(err)
	}
	caller := fixture.caller(t)
	update := func() error {
		return CallerTransaction(fixture.ctx, caller, func(transaction *CallerTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
			_, err := CallerTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 252), mutationHistoryRuntimeUpdate(fixture, "committed"))
			return err
		})
	}
	if err := update(); err == nil || !strings.Contains(err.Error(), "writer-semantic-drain-refusal") {
		t.Fatalf("expected the actual post-seal queue insertion to fail: %v", err)
	}
	title, rowVersion, clockVersion := readVersion()
	if title != beforeTitle || rowVersion != beforeVersion || clockVersion != beforeClock {
		t.Fatalf("semantic flush failure leaked application/version writes: title=%q version=%d clock=%d", title, rowVersion, clockVersion)
	}
	assertEmpty()
	if committed.Load() != 0 || embedder.calls.Load() != 0 {
		t.Fatal("rollback ran an after-commit hook or embedding provider")
	}
	if _, err := fixture.app.database.ExecContext(fixture.ctx, `DROP TRIGGER "reject_history_semantic_drain"`); err != nil {
		t.Fatal(err)
	}
	if err := update(); err != nil {
		t.Fatal(err)
	}
	mutationHistoryWriterGroup(t, fixture, map[string]bool{}, 1)
	title, rowVersion, clockVersion = readVersion()
	if title != "committed" || rowVersion <= beforeVersion || clockVersion < rowVersion {
		t.Fatalf("retry did not commit a coherent row/version: title=%q version=%d clock=%d", title, rowVersion, clockVersion)
	}
	var pending, jobs, vectorCount int
	if err := fixture.app.database.GetContext(fixture.ctx, &pending, "SELECT COUNT(*) FROM "+shadow+" WHERE status='pending'"); err != nil || pending != 1 {
		t.Fatalf("retry semantic shadow pending=%d err=%v", pending, err)
	}
	if err := fixture.app.database.GetContext(fixture.ctx, &jobs, `SELECT COUNT(*) FROM "golem_queue" WHERE type=? AND status='pending'`, semanticDrainJobType); err != nil || jobs != 1 {
		t.Fatalf("retry semantic drain jobs=%d err=%v", jobs, err)
	}
	if err := fixture.app.database.GetContext(fixture.ctx, &vectorCount, "SELECT COUNT(*) FROM "+vectors); err != nil || vectorCount != 0 {
		t.Fatalf("unstarted worker unexpectedly produced vectors=%d err=%v", vectorCount, err)
	}
	if committed.Load() != 1 || embedder.calls.Load() != 0 {
		t.Fatalf("retry after-commit hooks=%d embedding calls=%d", committed.Load(), embedder.calls.Load())
	}
}
