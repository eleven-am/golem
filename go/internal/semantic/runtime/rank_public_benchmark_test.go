package runtime_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
	"github.com/eleven-am/golem/go/internal/semantic/runtime/testdata/semanticbenchmark"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	providerapi "github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/jmoiron/sqlx"
)

var benchmarkSemanticResultCount int

type benchmarkEmbeddingProvider struct {
	specification embedding.Specification
	vector        embedding.Vector
}

func (provider benchmarkEmbeddingProvider) Specification() embedding.Specification {
	return provider.specification
}

func (provider benchmarkEmbeddingProvider) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	result := make([]embedding.Vector, len(inputs))
	for position := range result {
		result[position] = provider.vector
	}
	return result, nil
}

func BenchmarkGeneratedSQLiteSemanticSearch1024(b *testing.B) {
	for _, rows := range []int{20_000, 200_000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			benchmarkGeneratedSemanticCorpus(b, rows)
		})
	}
}

func benchmarkGeneratedSemanticCorpus(b *testing.B, count int) {
	b.Helper()
	ctx := context.Background()
	database, _, err := sqliteprovider.New().Open(ctx, "file:"+filepath.Join(b.TempDir(), "semantic.db"))
	if err != nil {
		b.Fatal(err)
	}
	schema := benchmarkSQLiteSchema(b)
	if err := sqliteprovider.New().ApplyInitial(ctx, database, schema); err != nil {
		b.Fatal(err)
	}

	components := make([]float32, 1024)
	components[0] = 1
	vector, err := embedding.NewVector(components)
	if err != nil {
		b.Fatal(err)
	}
	serialized, err := sqlitevec.Serialize(components, len(components))
	if err != nil {
		b.Fatal(err)
	}
	specification, err := embedding.NewSpecification("benchmark", "deterministic", "v1", 1024, 256)
	if err != nil {
		b.Fatal(err)
	}
	provider := benchmarkEmbeddingProvider{specification: specification, vector: vector}
	registry, err := embedding.NewRegistry(map[string]embedding.Provider{"benchmark": provider})
	if err != nil {
		b.Fatal(err)
	}
	inventory, err := semanticruntime.NewInventory(schema, registry)
	if err != nil {
		b.Fatal(err)
	}
	indexes := inventory.Indexes()
	if len(indexes) != 1 {
		b.Fatalf("semantic indexes=%d want=1", len(indexes))
	}
	seedGeneratedSemanticCorpus(b, database, schema, indexes[0].SpaceFingerprint, serialized, count)

	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)
	internal := providerhandle.AdoptUnverifiedForTest(database, providerhandle.TestMetadata{
		Provider: golem.SQLite, MaximumOpen: 8, MaximumIdle: 8,
	})
	handle := (*providerapi.Database)(internal)
	b.Cleanup(func() { _ = handle.Close() })
	application, err := semanticbenchmark.Open(ctx, semanticbenchmark.Config[semanticbenchmark.Principal]{
		Database:   handle,
		Embeddings: registry,
		Queue:      &golemruntime.QueueConfig{Registry: queue.NewRegistry()},
		ResolvePrincipal: func(context.Context, semanticbenchmark.Principal) (semanticbenchmark.Actor, error) {
			return semanticbenchmark.Actor{Readable: true}, nil
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	caller, err := application.ForPrincipal(ctx, semanticbenchmark.Principal{})
	if err != nil {
		b.Fatal(err)
	}
	selective := semanticbenchmark.Documents.Mailbox.Eq(0)
	source := semanticbenchmark.Documents.ByID.Value("000001")
	benchmarks := []struct {
		name string
		run  func() ([]golem.SemanticResult[semanticbenchmark.Document], error)
	}{
		{name: "Search/unfiltered", run: func() ([]golem.SemanticResult[semanticbenchmark.Document], error) {
			return caller.Documents.SearchContent(ctx, "query", 20)
		}},
		{name: "Search/selective-0.1-percent", run: func() ([]golem.SemanticResult[semanticbenchmark.Document], error) {
			return caller.Documents.SearchContent(ctx, "query", 20, selective)
		}},
		{name: "Similar/unfiltered", run: func() ([]golem.SemanticResult[semanticbenchmark.Document], error) {
			return caller.Documents.SimilarContent(ctx, source, 20)
		}},
		{name: "Similar/selective-0.1-percent", run: func() ([]golem.SemanticResult[semanticbenchmark.Document], error) {
			return caller.Documents.SimilarContent(ctx, source, 20, selective)
		}},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			results, err := benchmark.run()
			if err != nil {
				b.Fatal(err)
			}
			if len(results) != 20 {
				b.Fatalf("results=%d want=20", len(results))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				results, err = benchmark.run()
				if err != nil {
					b.Fatal(err)
				}
				benchmarkSemanticResultCount = len(results)
			}
		})
	}
}

func benchmarkSQLiteSchema(b *testing.B) physical.PhysicalSchema {
	b.Helper()
	for _, document := range semanticbenchmark.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() != golem.SQLite {
			continue
		}
		schema, err := physical.CanonicalDecode(document.Schema().Bytes())
		if err != nil {
			b.Fatal(err)
		}
		return schema
	}
	b.Fatal("generated SQLite schema is absent")
	return physical.PhysicalSchema{}
}

func seedGeneratedSemanticCorpus(b *testing.B, database *sqlx.DB, schema physical.PhysicalSchema, fingerprint [32]byte, vector []byte, count int) {
	b.Helper()
	storage := semanticBenchmarkStorage(b, schema)
	transaction, err := database.Beginx()
	if err != nil {
		b.Fatal(err)
	}
	defer transaction.Rollback()
	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<?)
INSERT INTO "semantic_benchmark_documents" ("id","mailbox","body")
SELECT printf('%06d',n),n%1000,'document' FROM seq`,
			args: []any{count},
		},
		{
			query: `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<?)
INSERT INTO "` + string(storage) + `_state" ("record_key","source_hash","space_fingerprint","status","updated_at","id")
SELECT printf('golem-semantic-key:v1|10:s:6:%06d',n),x'01',?,'ready',1,printf('%06d',n) FROM seq`,
			args: []any{count, hex.EncodeToString(fingerprint[:])},
		},
		{
			query: `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<?)
INSERT INTO "` + string(storage) + `_vec" ("record_key","embedding")
SELECT printf('golem-semantic-key:v1|10:s:6:%06d',n),? FROM seq`,
			args: []any{count, vector},
		},
	}
	for _, statement := range statements {
		if _, err := transaction.Exec(statement.query, statement.args...); err != nil {
			b.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		b.Fatal(err)
	}
	if _, err := database.Exec(`ANALYZE`); err != nil {
		b.Fatal(err)
	}
}

func semanticBenchmarkStorage(b *testing.B, schema physical.PhysicalSchema) physical.PhysicalName {
	b.Helper()
	for _, extension := range schema.Extensions {
		descriptor, err := semanticstorage.Decode(extension)
		if err == nil && descriptor.Name == "content" {
			return descriptor.Storage
		}
	}
	b.Fatal("generated semantic storage is absent")
	return ""
}
