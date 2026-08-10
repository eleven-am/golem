package pipeline

import (
	"context"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type p8ReviewedSQLiteBuild struct {
	Result  Result
	History ReviewedMigration
}

// p8BuildWithReviewedSQLiteHistory gives external-style generated application
// fixtures the same immutable initial history required in production. The
// first build obtains the normalized provider schema; only the second,
// history-bound build is published or executed.
func p8BuildWithReviewedSQLiteHistory(t *testing.T, ctx context.Context, request Request) p8ReviewedSQLiteBuild {
	t.Helper()
	initial, err := Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Providers) != 1 || initial.Providers[0].Provider.Provider != compilerir.SQLite {
		t.Fatalf("reviewed SQLite fixture providers=%#v", initial.Providers)
	}
	history := p8InitialSQLiteHistory(t, initial.ModelFingerprint, initial.Providers[0].Schema)
	request.ReviewedMigrations = []ReviewedMigration{history}
	bound, err := Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return p8ReviewedSQLiteBuild{Result: bound, History: history}
}

func p8InitialSQLiteHistory(t *testing.T, modelFingerprint compilerir.Fingerprint, schema physical.PhysicalSchema) ReviewedMigration {
	t.Helper()
	desired, err := physical.Normalize(schema)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := physical.Normalize(physical.PhysicalSchema{
		Version: desired.Version, CanonicalVersion: desired.CanonicalVersion,
		Provider: desired.Provider, Namespace: desired.Namespace,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migration.Diff(empty, desired)
	if err != nil {
		t.Fatal(err)
	}
	emptyModelFingerprint, err := compilerir.ModelFingerprint(compilerir.CanonicalEmptyModel())
	if err != nil {
		t.Fatal(err)
	}
	allowlistFingerprint, err := physical.UnmanagedAllowlistFingerprint(desired)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{
		ID: "0001_initial", Operations: plan.Operations, Phases: plan.Phases,
		BeforeModel: migration.Digest(emptyModelFingerprint), AfterModel: migration.Digest(modelFingerprint),
		BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint,
		BeforeSnapshot: empty, AfterSnapshot: desired,
		UnmanagedAllowlistDigest: migration.Digest(allowlistFingerprint.String()),
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
	}
	script, err := sqliteprovider.New().RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	const path = "migrations/sqlite/0001_initial.sql"
	files := map[string][]byte{path: []byte(script.SQL())}
	entry.Files = []migration.FileChecksum{{Path: path, SHA256: migration.Checksum(files[path])}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest := migration.Manifest{
		FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion,
		HashAlgorithm: "sha256", GeneratorVersion: "p8-pipeline-acceptance", Provider: desired.Provider,
		Entries: []migration.ManifestEntry{entry},
	}
	if _, err := migration.EncodeManifest(manifest, files); err != nil {
		t.Fatal(err)
	}
	return ReviewedMigration{Manifest: manifest, Files: files}
}

func p8ApplyReviewedSQLiteHistory(t *testing.T, ctx context.Context, database *sqlx.DB, history ReviewedMigration) {
	t.Helper()
	if err := sqliteprovider.New().ApplyMigration(ctx, database, history.Manifest, history.Files); err != nil {
		t.Fatal(err)
	}
}
