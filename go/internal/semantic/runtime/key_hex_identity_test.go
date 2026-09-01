package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	"github.com/jmoiron/sqlx"
)

const hexIdentityUpper = "ABCDEF0123456789ABCDEF0123456789"
const hexIdentityLower = "abcdef0123456789abcdef0123456789"

func newHexIdentityFixture(t *testing.T) drainFixture {
	t.Helper()
	database, err := sqlitevec.Open("file:" + t.TempDir() + "/hexid.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	db := sqlx.NewDb(database, "sqlite3")
	if _, err := db.Exec(`
CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT);
CREATE TABLE "` + drainStateTable + `" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL) STRICT;
CREATE INDEX "_golem_semantic_semantic-post-related_state_stale" ON "` + drainStateTable + `" ("record_key" ASC) WHERE "status" <> 'ready';
CREATE VIRTUAL TABLE "` + drainVectorTable + `" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine);
INSERT INTO "posts" (id,title) VALUES ('` + hexIdentityUpper + `','alpha'),('` + hexIdentityLower + `','beta')`); err != nil {
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
	embedder := &deterministicProvider{specification: specification, database: db}
	registry, _ := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	inventory, err := NewInventory(schema, registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(db, ir.SQLite, schema, inventory)
	if err != nil {
		t.Fatal(err)
	}
	return drainFixture{db: db, manager: manager, embedder: embedder}
}

func TestSemanticHexShapedStringIdentitiesAreBothSearchable(t *testing.T) {
	fixture := newHexIdentityFixture(t)
	if err := fixture.manager.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := fixture.embedder.calls(); calls != 2 {
		t.Fatalf("embedded=%d want=2", calls)
	}
	if states := fixture.count(t, drainStateTable); states != 2 {
		t.Fatalf("shadow rows=%d want=2", states)
	}
	if vectors := fixture.count(t, drainVectorTable); vectors != 2 {
		t.Fatalf("vector rows=%d want=2", vectors)
	}
	for _, id := range []string{hexIdentityUpper, hexIdentityLower} {
		var mirrored string
		key, err := semantickey.Encode([]any{id})
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Get(&mirrored, `SELECT "id" FROM "`+drainStateTable+`" WHERE record_key=?`, key); err != nil {
			t.Fatalf("%q has no shadow row of its own: %v", id, err)
		}
		if mirrored != id {
			t.Fatalf("shadow row for %q mirrors %q", id, mirrored)
		}
		if status := fixture.status(t, id); status != "ready" {
			t.Fatalf("%q status=%q want=ready", id, status)
		}
	}
	ranked, err := fixture.manager.Query(context.Background(), "post", "related", "beta", textCandidates(`SELECT "id" AS "id" FROM "posts"`), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 2 {
		t.Fatalf("ranked=%d want=2", len(ranked))
	}
	if ranked[0].Key == ranked[1].Key {
		t.Fatalf("both ranks share key %q", ranked[0].Key)
	}
	first, ok := ranked[0].Identity[0].(string)
	if !ok || first != hexIdentityLower {
		t.Fatalf("nearest identity=%v want=%q", ranked[0].Identity, hexIdentityLower)
	}
}
