package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	"github.com/eleven-am/golem/go/internal/testenv"
)

type cascadeIndexFixture struct {
	fixture  mutationResultFixture
	embedder *semanticMarkEmbedder
	state    string
	vectors  string
}

func TestDeleteCascadeKeepsSemanticAndFullTextIndexesExactAcrossProviders(t *testing.T) {
	runCascadeIndexProfiles(t, "cascade_index", compilerir.ActionCascade, func(t *testing.T, indexed cascadeIndexFixture) {
		ctx, fixture := context.Background(), indexed.fixture
		seedCascadeUser(t, fixture, 3, "carol")
		for _, post := range []struct {
			id, author byte
			title      string
		}{{40, 2, "orchid one"}, {41, 2, "orchid two"}, {42, 1, "tulip"}, {43, 3, "orchid three"}} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post.id, golem.UUID{15: post.author}, post.title)); err != nil {
				t.Fatal(err)
			}
		}
		indexed.drain(t)
		for _, id := range []byte{40, 41, 42, 43} {
			indexed.assertIndexed(t, id, "ready", 1)
		}
		caller := mustMutationResultCaller(t, fixture)
		if _, err := CallerDelete(ctx, caller, fixture.userDescriptor, cascadeUserTarget(fixture, 2)); err != nil {
			t.Fatal(err)
		}
		if count, err := CallerDeleteMany(ctx, caller, fixture.userDescriptor, fixture.userID.Eq(golem.UUID{15: 3})); err != nil || count != 1 {
			t.Fatalf("delete-many count=%d err=%v", count, err)
		}
		for _, id := range []byte{40, 41, 43} {
			if status, _, present := indexed.stateRow(t, id); !present || status == "ready" {
				t.Fatalf("cascaded post %d semantic state=%q present=%t; an ordinary delete marks it", id, status, present)
			}
		}
		indexed.drain(t)
		for _, id := range []byte{40, 41, 43} {
			indexed.assertIndexed(t, id, "", 0)
		}
		indexed.assertIndexed(t, 42, "ready", 1)
		rows, err := SystemTextSearch(ctx, fixture.app.System(), fixture.postDescriptor, runtimeFullTextIndexName, "orchid", 10)
		if err != nil || len(rows) != 0 {
			t.Fatalf("full-text entries of cascaded posts rows=%d err=%v", len(rows), err)
		}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(40, golem.UUID{15: 1}, "tulip again")); err != nil {
			t.Fatal(err)
		}
		rows, err = SystemTextSearch(ctx, fixture.app.System(), fixture.postDescriptor, runtimeFullTextIndexName, "orchid", 10)
		if err != nil || len(rows) != 0 {
			t.Fatalf("a reused identity matched the cascaded row's full-text entry rows=%d err=%v", len(rows), err)
		}
	})
}

func TestDeleteSetNullMarksSemanticDependentsAcrossProviders(t *testing.T) {
	runCascadeIndexProfiles(t, "setnull_index", compilerir.ActionSetNull, func(t *testing.T, indexed cascadeIndexFixture) {
		ctx, fixture := context.Background(), indexed.fixture
		for _, post := range []struct{ id, author byte }{{50, 2}, {51, 1}} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post.id, golem.UUID{15: post.author}, "lily")); err != nil {
				t.Fatal(err)
			}
		}
		indexed.drain(t)
		embedded := indexed.embedder.count()
		if _, err := SystemDelete(ctx, fixture.app.System(), fixture.userDescriptor, cascadeUserTarget(fixture, 2)); err != nil {
			t.Fatal(err)
		}
		if status, _, present := indexed.stateRow(t, 50); !present || status == "ready" {
			t.Fatalf("set-null dependent semantic state=%q present=%t; an ordinary update marks it", status, present)
		}
		indexed.assertIndexed(t, 51, "ready", 1)
		indexed.drain(t)
		indexed.assertIndexed(t, 50, "ready", 1)
		if indexed.embedder.count() != embedded {
			t.Fatalf("unchanged indexed text was re-embedded: calls=%d before=%d", indexed.embedder.count(), embedded)
		}
	})
}

func runCascadeIndexProfiles(t *testing.T, prefix string, action compilerir.ReferentialAction, operation func(*testing.T, cascadeIndexFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		provider := sqliteprovider.New()
		database, _, err := provider.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), prefix+".db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		schemaFixture := withCascadeIndexes(t, schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, action))
		if err := provider.ApplyInitial(context.Background(), database, schemaFixture.SQLite); err != nil {
			t.Fatal(err)
		}
		seedMutationBoundaryUsers(t, database, golem.SQLite, "")
		embedder := &semanticMarkEmbedder{}
		fixture := mutationResultFixtureForSchemaConfigured(t, database, golem.SQLite, schemaFixture, configureSemanticApp(t, embedder))
		operation(t, newCascadeIndexFixture(fixture, embedder, ""))
	})
	t.Run("postgresql-pgvector", func(t *testing.T) {
		dsn := testenv.DisposablePGVector(t)
		sequence := mutationOutboxNamespaceSequence.Add(1)
		applicationNamespace := fmt.Sprintf("golem_p4_%s_%d_%d", prefix, os.Getpid(), sequence)
		systemNamespace := fmt.Sprintf("golem_p4_%s_system_%d_%d", prefix, os.Getpid(), sequence)
		schemaFixture := withCascadeIndexes(t, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(action)(t, physical.PhysicalName(applicationNamespace), physical.PhysicalName(systemNamespace)))
		provider := postgresprovider.New()
		database, _, err := provider.Open(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := provider.ApplyInitial(context.Background(), database, schemaFixture.PostgreSQL); err != nil {
			t.Fatal(err)
		}
		seedMutationBoundaryUsers(t, database, golem.PostgreSQL, applicationNamespace)
		embedder := &semanticMarkEmbedder{}
		fixture := mutationResultFixtureForSchemaConfigured(t, database, golem.PostgreSQL, schemaFixture, configureSemanticApp(t, embedder))
		operation(t, newCascadeIndexFixture(fixture, embedder, quoteAcceptanceIdentifier(applicationNamespace)+"."))
	})
}

func withCascadeIndexes(t testing.TB, base schematest.Fixture) schematest.Fixture {
	t.Helper()
	fixture := withRuntimeFullTextIndex(t, schematest.WithSemanticIndex(t, base))
	registry, err := schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Registry = registry
	return fixture
}

func newCascadeIndexFixture(fixture mutationResultFixture, embedder *semanticMarkEmbedder, namespace string) cascadeIndexFixture {
	storage := ""
	for _, extension := range fixture.schema.SQLite.Extensions {
		if extension.Kind == semanticcontract.IndexKind {
			storage = "_golem_semantic_" + string(extension.ID)
		}
	}
	return cascadeIndexFixture{fixture: fixture, embedder: embedder, state: namespace + `"` + storage + `_state"`, vectors: namespace + `"` + storage + `_vec"`}
}

func (indexed cascadeIndexFixture) drain(t *testing.T) {
	t.Helper()
	for pass := 0; pass < 8; pass++ {
		more, err := indexed.fixture.app.semantic.Drain(context.Background(), semanticIndexModel(indexed.fixture.schema.Post), schematest.SemanticIndexName)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return
		}
	}
	t.Fatal("semantic drain did not settle")
}

func (indexed cascadeIndexFixture) stateRow(t *testing.T, id byte) (string, int, bool) {
	t.Helper()
	var status string
	var hashLength int
	query := indexed.fixture.app.database.Rebind(`SELECT "status", length("source_hash") FROM ` + indexed.state + ` WHERE "record_key" = ?`)
	if err := indexed.fixture.app.database.QueryRowxContext(context.Background(), query, semanticPostKey(t, id)).Scan(&status, &hashLength); err != nil {
		return "", 0, false
	}
	return status, hashLength, true
}

func (indexed cascadeIndexFixture) assertIndexed(t *testing.T, id byte, status string, vectors int) {
	t.Helper()
	got, _, present := indexed.stateRow(t, id)
	if status == "" && present || status != "" && got != status {
		t.Fatalf("post %d semantic state=%q present=%t want=%q", id, got, present, status)
	}
	var count int
	query := indexed.fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + indexed.vectors + ` WHERE "record_key" = ?`)
	if err := indexed.fixture.app.database.GetContext(context.Background(), &count, query, semanticPostKey(t, id)); err != nil {
		t.Fatal(err)
	}
	if count != vectors {
		t.Fatalf("post %d vectors=%d want=%d", id, count, vectors)
	}
}
