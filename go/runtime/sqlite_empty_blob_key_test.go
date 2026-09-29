package runtime

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type bytesKeyedReader struct {
	fixture mutationResultFixture
	users   golem.ModelDescriptor[mutationResultUser]
	posts   golem.ModelDescriptor[mutationResultPost]
	postID  golem.BytesField[mutationResultPost]
	title   golem.TextField[mutationResultPost, string]
	written golem.ToMany[mutationResultUser, mutationResultPost]
}

func newBytesKeyedReader(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture, configure func(*Config[mutationResultPrincipal, mutationResultActor])) bytesKeyedReader {
	t.Helper()
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID, schema.PostTitle)
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID, schema.UserName)
	userRelation := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	posts := golem.GeneratedModelDescriptor[mutationResultPost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostID, schema.AuthorID, schema.PostAuthorName, schema.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation}))
	users := golem.GeneratedModelDescriptor[mutationResultUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation}))
	fixture := mutationResultFixtureForSchemaConfigured(t, database, provider, schema, func(config *Config[mutationResultPrincipal, mutationResultActor]) {
		descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(),
			golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), users.Metadata(), posts.Metadata()))
		if err != nil {
			t.Fatal(err)
		}
		config.Descriptors = descriptors
		if configure != nil {
			configure(config)
		}
	})
	return bytesKeyedReader{
		fixture: fixture, users: users, posts: posts,
		postID:  golem.GeneratedBytesField[mutationResultPost](schema.PostID),
		title:   golem.GeneratedTextField[mutationResultPost, string](schema.PostTitle),
		written: golem.GeneratedToMany[mutationResultUser, mutationResultPost](schema.UserPosts, schema.Authorship, schema.Post),
	}
}

func seedBytesKeyedRows(t *testing.T, database *sqlx.DB, provider policyir.Provider, prefix string) {
	t.Helper()
	empty, one := "x''", "x'01'"
	if provider == policyir.ProviderPostgreSQL {
		empty, one = `'\x'::bytea`, `'\x01'::bytea`
	}
	statements := []string{
		`INSERT INTO ` + prefix + `"users" ("id","name") VALUES (` + empty + `,'alice')`,
		`INSERT INTO ` + prefix + `"posts" ("id","author_id","author_name","title") VALUES (` + empty + `,` + empty + `,'alice','alice')`,
		`INSERT INTO ` + prefix + `"posts" ("id","author_id","author_name","title") VALUES (` + one + `,` + empty + `,'alice','alice')`,
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func (reader bytesKeyedReader) assertIDs(t *testing.T, path string, rows []golem.Row[mutationResultPost]) {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		id, present := golem.Value(row, reader.postID).Get()
		title, titlePresent := golem.Value(row, reader.title).Get()
		if !present || id == nil || !titlePresent || title != "alice" {
			t.Fatalf("%s row id=%#v present=%t title=%q present=%t", path, id, present, title, titlePresent)
		}
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "" || ids[1] != "\x01" {
		t.Fatalf("%s ids=%q; want the empty key and 01", path, ids)
	}
}

func TestEmptyBytesKeysReadCorrectlyThroughBatchLoadsAndFullTextSearch(t *testing.T) {
	runExactValueProfilesFor(t, func(t testing.TB) schematest.Fixture {
		return withRuntimeFullTextIndex(t, schematest.NewBytesKeyed(t))
	}, func(t *testing.T, profile exactValueProfile) {
		ctx := context.Background()
		provider := golem.SQLite
		if profile.provider == policyir.ProviderPostgreSQL {
			provider = golem.PostgreSQL
		}
		seedBytesKeyedRows(t, profile.database, profile.provider, profile.prefix)
		reader := newBytesKeyedReader(t, profile.database, provider, profile.fixture, nil)
		system := reader.fixture.app.System()
		batched := context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched)
		users, err := SystemFindMany(batched, system, reader.users, golem.Select[mutationResultUser](reader.written.Args(golem.Select[mutationResultPost](reader.postID))))
		if err != nil || len(users) != 1 {
			t.Fatalf("batched relation load keyed by bytes users=%d err=%v", len(users), err)
		}
		children, present := golem.Many(users[0], reader.written).Get()
		if !present || len(children) != 2 {
			t.Fatalf("batched relation load keyed by bytes children=%d present=%t", len(children), present)
		}
		ids := []string{}
		for _, child := range children {
			id, idPresent := golem.Value(child, reader.postID).Get()
			if !idPresent || id == nil {
				t.Fatalf("batched child id=%#v present=%t", id, idPresent)
			}
			ids = append(ids, string(id))
		}
		sort.Strings(ids)
		if ids[0] != "" || ids[1] != "\x01" {
			t.Fatalf("batched child ids=%q", ids)
		}
		results, err := SystemTextSearchSelect(ctx, system, reader.posts, runtimeFullTextIndexName, "alice", 10, golem.Select[mutationResultPost](reader.postID, reader.title))
		if err != nil {
			t.Fatalf("full-text search over an empty bytes key: %v", err)
		}
		rows := make([]golem.Row[mutationResultPost], len(results))
		for index, result := range results {
			rows[index] = result.Row()
		}
		reader.assertIDs(t, "full-text search", rows)
	})
}

func TestEmptyBytesKeysReadCorrectlyThroughSemanticReconcileAndSearch(t *testing.T) {
	ctx := context.Background()
	schema := schematest.WithSemanticIndex(t, schematest.NewBytesKeyed(t))
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "bytes-keyed-semantic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
		t.Fatal(err)
	}
	seedBytesKeyedRows(t, database, policyir.ProviderSQLite, "")
	embedder := &semanticMarkEmbedder{}
	reader := newBytesKeyedReader(t, database, golem.SQLite, schema, configureSemanticApp(t, embedder))
	if err := reader.fixture.app.RefreshSemanticIndexes(ctx); err != nil {
		t.Fatalf("semantic reconcile over an empty bytes key: %v", err)
	}
	if embedder.count() != 2 {
		t.Fatalf("semantic reconcile embedded %d records, want 2", embedder.count())
	}
	results, err := SystemSearch(ctx, reader.fixture.app.System(), reader.posts, schematest.SemanticIndexName, "alice", 10)
	if err != nil {
		t.Fatalf("semantic search over an empty bytes key: %v", err)
	}
	rows := make([]golem.Row[mutationResultPost], len(results))
	for index, result := range results {
		rows[index] = result.Row()
	}
	reader.assertIDs(t, "semantic search", rows)
}
