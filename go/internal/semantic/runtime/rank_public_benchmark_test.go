package runtime_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
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

func TestGeneratedSemanticBenchmarkFixtureMatchesGenerator(t *testing.T) {
	directory, err := filepath.Abs("testdata/semanticbenchmark")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := pipeline.Build(context.Background(), pipeline.Request{
		Compile: compile.Config{Dir: directory, Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{
			ImportPath:  "github.com/eleven-am/golem/go/internal/semantic/runtime/testdata/semanticbenchmark",
			PackageName: "semanticbenchmark", Directory: directory,
		},
		Lowerers: []physical.Lowerer{sqliteprovider.New()},
		Env:      append(os.Environ(), "GOWORK=off"),
	})
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := filepath.Clean(filepath.Join(directory, "..", "..", "..", "..", ".."))
	expected := make(map[string]bool)
	for _, artifact := range generated.Prospective.Artifacts {
		if !strings.HasSuffix(artifact.Path, ".gen.go") {
			continue
		}
		expected[filepath.Base(artifact.Path)] = true
		committed, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(committed, artifact.Content) {
			t.Errorf("generated benchmark fixture is stale: %s", artifact.Path)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".gen.go") && !expected[entry.Name()] {
			t.Errorf("obsolete generated benchmark fixture: %s", entry.Name())
		}
	}
}

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
			benchmarkGeneratedSemanticCorpus(b, rows, sqliteprovider.DurabilityWAL)
		})
	}
}

func BenchmarkGeneratedSQLiteNetworkSemanticSearch1024(b *testing.B) {
	for _, rows := range []int{3_864, 20_000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			benchmarkGeneratedSemanticCorpus(b, rows, sqliteprovider.DurabilityNetworkFilesystem)
		})
	}
}

func benchmarkGeneratedSemanticCorpus(b *testing.B, count int, durability sqliteprovider.DurabilityProfile) {
	b.Helper()
	ctx := context.Background()
	database, _, err := sqliteprovider.New().OpenWithDurability(ctx, "file:"+filepath.Join(b.TempDir(), "semantic.db"), durability)
	if err != nil {
		b.Fatal(err)
	}
	var handle *providerapi.Database
	b.Cleanup(func() {
		if handle != nil {
			_ = handle.Close()
		} else {
			_ = database.Close()
		}
	})
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

	pool := database.Stats().MaxOpenConnections
	var journal string
	if err := database.GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil {
		b.Fatal(err)
	}
	if durability == sqliteprovider.DurabilityNetworkFilesystem && (pool != 1 || journal != "delete") {
		b.Fatalf("network-filesystem profile: pool=%d journal=%s", pool, journal)
	}
	b.Logf("temporary corpus: rows=%d dimensions=1024 pool=%d journal=%s; storage filesystem follows testing.TempDir; go=%s os=%s arch=%s GOMAXPROCS=%d", count, pool, journal, goruntime.Version(), goruntime.GOOS, goruntime.GOARCH, goruntime.GOMAXPROCS(0))
	b.Log("cache conditions: freshly seeded database, no OS/SQLite cache flush; subcases share the corpus. The first query below is first observed for that subcase, not a cold-cache or NAS latency measurement.")
	internal := providerhandle.AdoptUnverifiedForTest(database, providerhandle.TestMetadata{
		Provider: golem.SQLite, MaximumOpen: pool, MaximumIdle: pool,
	})
	handle = (*providerapi.Database)(internal)
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
		want int
		run  func() ([]golem.SemanticResult[semanticbenchmark.Document], error)
	}{
		{name: "Search/unfiltered", want: 20, run: func() ([]golem.SemanticResult[semanticbenchmark.Document], error) {
			return caller.Documents.SearchContent(ctx, "query", 20)
		}},
		{name: "Search/selective-0.1-percent", want: min(20, count/1000), run: func() ([]golem.SemanticResult[semanticbenchmark.Document], error) {
			return caller.Documents.SearchContent(ctx, "query", 20, selective)
		}},
		{name: "Similar/unfiltered", want: 20, run: func() ([]golem.SemanticResult[semanticbenchmark.Document], error) {
			return caller.Documents.SimilarContent(ctx, source, 20)
		}},
		{name: "Similar/selective-0.1-percent", want: min(20, count/1000), run: func() ([]golem.SemanticResult[semanticbenchmark.Document], error) {
			return caller.Documents.SimilarContent(ctx, source, 20, selective)
		}},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			firstStarted := time.Now()
			results, err := benchmark.run()
			firstElapsed := time.Since(firstStarted)
			if err != nil {
				b.Fatal(err)
			}
			if len(results) != benchmark.want {
				b.Fatalf("results=%d want=%d", len(results), benchmark.want)
			}
			// Bound retained measurements independently of benchtime. Quantiles
			// describe the first min(iterations, 4096) post-first-query calls;
			// max covers every loop call. No reservoir or percentile estimator.
			const sampleLimit = 4096
			samples := make([]time.Duration, 0, sampleLimit)
			var warmMax time.Duration
			var iterations int64
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				started := time.Now()
				results, err = benchmark.run()
				elapsed := time.Since(started)
				iterations++
				warmMax = max(warmMax, elapsed)
				if len(samples) < sampleLimit {
					samples = append(samples, elapsed)
				}
				if err != nil {
					b.Fatal(err)
				}
				if len(results) != benchmark.want {
					b.Fatalf("results=%d want=%d", len(results), benchmark.want)
				}
				benchmarkSemanticResultCount = len(results)
			}
			b.StopTimer()
			slices.Sort(samples)
			b.ReportMetric(float64(firstElapsed.Nanoseconds()), "first-query-ns")
			b.ReportMetric(float64(len(samples)), "warm-samples")
			b.ReportMetric(float64(iterations), "warm-iterations")
			b.ReportMetric(float64(warmMax.Nanoseconds()), "warm-max-ns")
			if len(samples) != 0 {
				// Nearest-rank quantiles; one observation has identical quantiles.
				for _, percentile := range []int{50, 95, 99} {
					rank := (percentile*len(samples) + 99) / 100
					b.ReportMetric(float64(samples[rank-1].Nanoseconds()), fmt.Sprintf("warm-p%d-ns", percentile))
				}
			}
			b.Logf("latency samples=%d iterations=%d cap=%d; quantiles use first samples, max uses all calls; ordinary ns/op includes timing/accounting overhead; no cache flush", len(samples), iterations, sampleLimit)
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
