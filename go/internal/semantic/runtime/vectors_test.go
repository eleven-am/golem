package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func vectorTestCandidates(sql string, args ...any) Candidates {
	value := textCandidates(sql, args...)
	value.MaxStatementParameters = 999
	return value
}

func TestStoredVectorReadUsesAuthorizationReadinessRevisionAndTransactionSnapshot(t *testing.T) {
	fixture := newDrainFixture(t)
	candidates := vectorTestCandidates(`SELECT "id" AS "id" FROM "posts"`)
	sources := VectorSources{SQL: `SELECT 0 AS ordinal,id FROM posts WHERE id=?1 UNION ALL SELECT 1 AS ordinal,id FROM posts WHERE id=?2 UNION ALL SELECT 2 AS ordinal,id FROM posts WHERE id=?3`, Args: []any{"a", "b", "missing"}, Columns: []string{"id"}, PositionColumn: "ordinal", Count: 3}
	before := fixture.embedder.calls()
	fixture.embedder.panicEmbed = true
	vectors, err := fixture.manager.VectorsOn(context.Background(), fixture.db, "post", "related", candidates, sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || !reflect.DeepEqual(vectors[0].Vector().Values(), []float32{1, 0, 0}) || !reflect.DeepEqual(vectors[1].Vector().Values(), []float32{0, 1, 0}) || vectors[0].Specification() != fixture.embedder.specification {
		t.Fatalf("stored vectors=%#v", vectors)
	}
	vectors, err = fixture.manager.VectorsOn(context.Background(), fixture.db, "post", "related", vectorTestCandidates(`SELECT id FROM posts WHERE id='a'`), sources)
	if err != nil || len(vectors) != 1 || vectors[0].Validate() != nil {
		t.Fatalf("authorized sources=%#v err=%v", vectors, err)
	}
	transaction, err := fixture.db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.Exec(`DELETE FROM posts WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	vectors, err = fixture.manager.VectorsOn(context.Background(), transaction, "post", "related", candidates, sources)
	if err != nil || len(vectors) != 1 {
		t.Fatalf("transaction sources=%#v err=%v", vectors, err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	fixture.mark(t, "a")
	vectors, err = fixture.manager.VectorsOn(context.Background(), fixture.db, "post", "related", candidates, sources)
	if err != nil || len(vectors) != 1 || vectors[1].Validate() != nil {
		t.Fatalf("pending sources=%#v err=%v", vectors, err)
	}
	if _, err := fixture.db.Exec(`UPDATE "` + drainStateTable + `" SET space_fingerprint='obsolete' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	vectors, err = fixture.manager.VectorsOn(context.Background(), fixture.db, "post", "related", candidates, sources)
	if err != nil || len(vectors) != 0 {
		t.Fatalf("obsolete sources=%#v err=%v", vectors, err)
	}
	if fixture.embedder.calls() != before {
		t.Fatal("stored vector retrieval invoked an embedding provider")
	}
}

func TestStoredVectorReadRejectsAmbiguousSourcesBeforeReadinessFiltering(t *testing.T) {
	fixture := newDrainFixture(t)
	fixture.mark(t, "a")
	sources := VectorSources{SQL: `SELECT 0 AS ordinal,id FROM posts`, Columns: []string{"id"}, PositionColumn: "ordinal", Count: 1}
	_, err := fixture.manager.VectorsOn(context.Background(), fixture.db, "post", "related", vectorTestCandidates(`SELECT id FROM posts`), sources)
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
		t.Fatalf("ambiguous source returned a ready vector: %v", err)
	}
}

func TestStoredVectorCodecsRejectMalformedValues(t *testing.T) {
	specification, err := embedding.NewSpecification("test", "model", "v1", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{Specification: specification}
	manager := &Manager{provider: ir.PostgreSQL}
	vector, err := manager.decodeStoredVector(index, []byte(`[1,0,0]`))
	if err != nil || !reflect.DeepEqual(vector.Vector().Values(), []float32{1, 0, 0}) {
		t.Fatalf("PostgreSQL stored vector codec: %v", err)
	}
	for _, encoded := range []string{`[]`, `[1,0]`, `[0,0,0]`, `[NaN,0,0]`, `[1e100,0,0]`, `[1e-10,0,0]`, `[1e10,0,0]`} {
		if _, err := manager.decodeStoredVector(index, []byte(encoded)); err == nil {
			t.Fatalf("malformed PostgreSQL vector accepted: %s", encoded)
		}
	}
	fixture := newDrainFixture(t)
	if _, err := fixture.db.Exec(`UPDATE "` + drainVectorTable + `" SET embedding=x''`); err != nil {
		t.Fatal(err)
	}
	sources := VectorSources{SQL: `SELECT 0 AS ordinal,id FROM posts WHERE id='a'`, Columns: []string{"id"}, PositionColumn: "ordinal", Count: 1}
	if _, err := fixture.manager.VectorsOn(context.Background(), fixture.db, "post", "related", vectorTestCandidates(`SELECT id FROM posts`), sources); err == nil {
		t.Fatal("empty stored SQLite vector was treated as ready")
	}
}
