package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
)

func TestQueryVectorRanksWithoutProviderCallsAndUsesTransactionSnapshot(t *testing.T) {
	fixture := newDrainFixture(t)
	vector, err := embedding.NewVector([]float32{0, 1, 0})
	if err != nil {
		t.Fatal(err)
	}
	query, err := embedding.NewQueryVector(fixture.embedder.specification, vector)
	if err != nil {
		t.Fatal(err)
	}
	before := fixture.embedder.calls()
	fixture.embedder.panicEmbed = true
	candidates := textCandidates(`SELECT "id" AS "id" FROM "posts"`)
	ranks, err := fixture.manager.QueryVectorOn(context.Background(), fixture.db, "post", "related", query, candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := semantickey.Encode([]any{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 2 || ranks[0].Key != beta || ranks[0].Distance != 0 || ranks[1].Distance != 1 {
		t.Fatalf("prepared vector ranks=%#v", ranks)
	}
	transaction, err := fixture.db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.Exec(`DELETE FROM "posts" WHERE "id"='b'`); err != nil {
		t.Fatal(err)
	}
	ranks, err = fixture.manager.QueryVectorOn(context.Background(), transaction, "post", "related", query, candidates, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks) != 1 || ranks[0].Key == beta || ranks[0].Distance != 1 {
		t.Fatalf("transaction candidate snapshot ignored: %#v", ranks)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	ranks, err = fixture.manager.QueryVectorOn(context.Background(), fixture.db, "post", "related", query, candidates, 10)
	if err != nil || len(ranks) != 2 || ranks[0].Key != beta {
		t.Fatalf("rollback ranks=%#v err=%v", ranks, err)
	}
	if fixture.embedder.calls() != before {
		t.Fatal("prepared vector ranking invoked the embedding provider")
	}
}

func TestQueryVectorRejectsSpecificationAndNormMismatchBeforeSQL(t *testing.T) {
	fixture := newDrainFixture(t)
	base := fixture.embedder.specification
	for _, change := range []struct {
		name, provider, model, revision string
		dimensions, batch               int
	}{
		{"provider", "another", base.Model(), base.Revision(), 3, 8},
		{"model", base.Provider(), "another", base.Revision(), 3, 8},
		{"revision", base.Provider(), base.Model(), "another", 3, 8},
		{"dimensions", base.Provider(), base.Model(), base.Revision(), 2, 8},
		{"specification", base.Provider(), base.Model(), base.Revision(), 3, 7},
	} {
		t.Run(change.name, func(t *testing.T) {
			specification, err := embedding.NewSpecification(change.provider, change.model, change.revision, change.dimensions, change.batch)
			if err != nil {
				t.Fatal(err)
			}
			values := make([]float32, change.dimensions)
			values[0] = 1
			vector, err := embedding.NewVector(values)
			if err != nil {
				t.Fatal(err)
			}
			query, err := embedding.NewQueryVector(specification, vector)
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixture.manager.QueryVectorOn(context.Background(), fixture.db, "post", "related", query, textCandidates(`SELECT "id" FROM table_that_must_not_be_queried`), 1)
			if err == nil || !strings.Contains(err.Error(), "does not match the index embedding specification") {
				t.Fatalf("mismatched specification reached SQL: %v", err)
			}
		})
	}
	for _, value := range []float32{1e-10, 1e10} {
		vector, err := embedding.NewVector([]float32{value, 0, 0})
		if err != nil {
			t.Fatal(err)
		}
		query, err := embedding.NewQueryVector(base, vector)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fixture.manager.QueryVectorOn(context.Background(), fixture.db, "post", "related", query, textCandidates(`SELECT "id" FROM table_that_must_not_be_queried`), 1)
		if err == nil || !strings.Contains(err.Error(), "cannot be encoded for exact cosine distance") {
			t.Fatalf("unsafe cosine norm reached SQL: %v", err)
		}
	}
}
