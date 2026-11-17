package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/jmoiron/sqlx"
)

func sqliteSnapshotFixture(t *testing.T) (*sqlx.DB, physical.PhysicalSchema, migration.Manifest) {
	t.Helper()
	return sqliteSnapshotFixtureForSchema(t, normalizationFullTextSchema(t, foreignKeyMigrationFixtureSchema(t, false), fulltextcontract.NormalizationNFCLower))
}

func sqliteSnapshotFixtureForSchema(t *testing.T, schema physical.PhysicalSchema) (*sqlx.DB, physical.PhysicalSchema, migration.Manifest) {
	t.Helper()
	ctx := context.Background()
	provider := New()
	manifest, files := migrationFixtureManifest(t, canonicalEmptyMigrationSchema(t, schema), schema, "0001_initial.sql", nil)
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO items(id,name) VALUES(1,'snapshot alpha'); INSERT INTO children(id,item_id) VALUES(2,1)"); err != nil {
		t.Fatal(err)
	}
	return database, schema, manifest
}

func TestSQLiteSnapshotPrivateVerifierRegistersProviderFunctionsAndCollations(t *testing.T) {
	schema := normalizationFullTextSchema(t, foreignKeyMigrationFixtureSchema(t, false), fulltextcontract.NormalizationNFCLower)
	schema.Unmanaged = append(schema.Unmanaged,
		physical.UnmanagedObject{Kind: "table", Name: "snapshot_udfs"},
		physical.UnmanagedObject{Kind: "index", Name: "snapshot_udfs_index"},
	)
	schema = normalizeMigrationFixture(t, schema)
	source, schema, manifest := sqliteSnapshotFixtureForSchema(t, schema)
	integer, err := policyir.NewTypeRef(policyir.ValueInt64, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	item, err := policyir.SignedValue(policyir.ValueInt64, 9007199254740993)
	if err != nil {
		t.Fatal(err)
	}
	list, err := encodePolicyList([]policyir.Value{item}, integer, nil)
	if err != nil {
		t.Fatal(err)
	}
	number, err := policyir.NewJSONNumber(false, []byte("9007199254740993"), 0)
	if err != nil {
		t.Fatal(err)
	}
	numberValue, err := policyir.JSONNumberValueOf(number)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := encodePolicyJSON(numberValue)
	if err != nil {
		t.Fatal(err)
	}
	member, err := policyir.NewJSONMember("n", numberValue)
	if err != nil {
		t.Fatal(err)
	}
	objectValue, err := policyir.JSONObjectValue([]policyir.JSONMember{member})
	if err != nil {
		t.Fatal(err)
	}
	object, err := encodePolicyJSON(objectValue)
	if err != nil {
		t.Fatal(err)
	}
	key, err := policyir.JSONKeySegment("n")
	if err != nil {
		t.Fatal(err)
	}
	path, err := policyir.NewJSONPath(key)
	if err != nil {
		t.Fatal(err)
	}
	encodedPath, err := encodePolicyPath(path)
	if err != nil {
		t.Fatal(err)
	}
	// Persistent CHECK/index definitions use the real provider codecs and IR
	// operator identities, including ListEqual's canonical array operand.
	definition := fmt.Sprintf(`CREATE TABLE snapshot_udfs(id INTEGER PRIMARY KEY,number TEXT NOT NULL CHECK(golem_analytics_numeric_compare_v1(number,'2')>0),label TEXT NOT NULL CHECK(golem_policy_ascii_fold(label)='abc'),list TEXT NOT NULL CHECK(golem_policy_list(list,%s,%s,%d)=1),object TEXT NOT NULL CHECK(golem_policy_json(object,%s,%s,%d,%d,%d)=1))`, quoteLiteral(list), quoteLiteral(policyListTypeDescriptor(integer)), policyir.OperatorListEqual, quoteLiteral(encodedPath), quoteLiteral(operand), policyir.OperatorJSONEqual, policyir.ComparisonSensitive, policyir.OperandOne)
	for _, statement := range []string{definition, `CREATE INDEX snapshot_udfs_index ON snapshot_udfs(number COLLATE golem_analytics_numeric_v1,golem_fulltext_nfc(label))`} {
		if _, err := source.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := source.Exec(`INSERT INTO snapshot_udfs VALUES(1,'10','ABC',?,?)`, list, object); err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	if err := New().Snapshot(context.Background(), source, schema, manifest, filepath.Join(directory, "valid.db")); err != nil {
		t.Fatalf("private verifier omitted provider registration: %v", err)
	}
	assertSQLiteSnapshotDirectory(t, directory, "valid.db")
	connection, err := source.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = connection.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=ON`)
	if err == nil {
		_, err = connection.ExecContext(context.Background(), `INSERT INTO snapshot_udfs VALUES(2,'1','ABC',?,?)`, list, object)
	}
	_, restoreErr := connection.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=OFF`)
	closeErr := connection.Close()
	if err != nil || restoreErr != nil || closeErr != nil {
		t.Fatalf("invalid CHECK fixture: error=%v restore=%v close=%v", err, restoreErr, closeErr)
	}
	var invalidNumber string
	if err := source.Get(&invalidNumber, `SELECT number FROM snapshot_udfs WHERE id=2`); err != nil || invalidNumber != "1" {
		t.Fatalf("invalid CHECK row missing: number=%q error=%v", invalidNumber, err)
	}
	if err := New().Snapshot(context.Background(), source, schema, manifest, filepath.Join(directory, "invalid.db")); err == nil || errors.Is(err, ErrSnapshotPublished) {
		t.Fatalf("provider CHECK violation was published: %v", err)
	}
	assertSQLiteSnapshotDirectory(t, directory, "valid.db")
}

func TestSQLiteSnapshotVerifierForbidsOrdinarySQLWrites(t *testing.T) {
	source, schema, manifest := sqliteSnapshotFixture(t)
	directory := t.TempDir()
	destination := filepath.Join(directory, "snapshot.db")
	if err := New().Snapshot(context.Background(), source, schema, manifest, destination); err != nil {
		t.Fatal(err)
	}
	verification, err := openSQLiteSnapshotVerification(destination)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = verification.Close() })
	var queryOnly int
	var mode string
	if err := verification.Get(&queryOnly, `PRAGMA query_only`); err != nil || queryOnly != 1 {
		t.Fatalf("verifier query_only=%d error=%v", queryOnly, err)
	}
	if err := verification.Get(&mode, `PRAGMA journal_mode`); err != nil || mode != "delete" {
		t.Fatalf("completed artifact journal mode=%q error=%v", mode, err)
	}
	if _, err := verification.Exec(`UPDATE items SET name='forbidden verifier write' WHERE id=1`); err == nil {
		t.Fatal("query-only verifier executed an ordinary write")
	}
	var name string
	if err := verification.Get(&name, `SELECT name FROM items WHERE id=1`); err != nil || name != "snapshot alpha" {
		t.Fatalf("verification changed copied data: name=%q error=%v", name, err)
	}
	if err := verification.Close(); err != nil {
		t.Fatal(err)
	}
	assertSQLiteSnapshotDirectory(t, directory, "snapshot.db")
}

func TestSQLiteSnapshotPreservesOptedDataIncarnationAndRefusesMissingSingleton(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "preserved"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			provider := New()
			schema := incarnationFixtureSchema(t, false)
			empty := normalizeMigrationFixture(t, physical.PhysicalSchema{Version: schema.Version, CanonicalVersion: schema.CanonicalVersion, Provider: schema.Provider, Namespace: schema.Namespace})
			manifest, artifacts := migrationFixtureManifest(t, empty, schema, "0001_initial.sql", nil)
			source, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "source.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if err := provider.ApplyMigration(ctx, source, manifest, artifacts); err != nil {
				t.Fatal(err)
			}
			before, err := provider.ReadDataIncarnation(ctx, source, schema.System)
			if err != nil || !before.Valid() {
				t.Fatalf("reviewed initialization: value=%s error=%v", before.String(), err)
			}
			if missing {
				if _, err := source.Exec(`DELETE FROM "_golem_data_incarnation"`); err != nil {
					t.Fatal(err)
				}
			}
			directory := t.TempDir()
			destination := filepath.Join(directory, "snapshot.db")
			err = provider.Snapshot(ctx, source, schema, manifest, destination)
			if missing {
				if err == nil || errors.Is(err, ErrSnapshotPublished) {
					t.Fatalf("unusable opted archive published: %v", err)
				}
				assertSQLiteSnapshotDirectory(t, directory)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			copyDatabase, err := openSQLiteSnapshotVerification(destination)
			if err != nil {
				t.Fatal(err)
			}
			defer copyDatabase.Close()
			copied, err := provider.ReadDataIncarnation(ctx, copyDatabase, schema.System)
			if err != nil || copied != before {
				t.Fatalf("copy changed identity: copied=%s error=%v", copied.String(), err)
			}
			after, err := provider.ReadDataIncarnation(ctx, source, schema.System)
			if err != nil || after != before {
				t.Fatalf("source identity changed: %v", err)
			}
			if err := copyDatabase.Close(); err != nil {
				t.Fatal(err)
			}
			assertSQLiteSnapshotDirectory(t, directory, "snapshot.db")
		})
	}
}

func TestSQLiteSnapshotVerifiesAndPublishesIndependentReviewedCopy(t *testing.T) {
	ctx := context.Background()
	source, schema, manifest := sqliteSnapshotFixture(t)
	directory := t.TempDir()
	destination := filepath.Join(directory, "snapshot.db")
	if err := New().Snapshot(ctx, source, schema, manifest, destination); err != nil {
		t.Fatal(err)
	}
	if err := New().verifySnapshot(ctx, destination, schema, manifest); err != nil {
		t.Fatalf("published copy failed query-only verification: %v", err)
	}
	assertSQLiteSnapshotDirectory(t, directory, "snapshot.db")
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot permissions info=%v error=%v", info, err)
	}
	if _, err := source.ExecContext(ctx, "UPDATE items SET name='later beta' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	copied, _, err := New().Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	var name string
	if err := copied.GetContext(ctx, &name, "SELECT name FROM items WHERE id=1"); err != nil || name != "snapshot alpha" {
		t.Fatalf("copy moved with source: name=%q error=%v", name, err)
	}
	var matches int
	fts := quote(physical.PhysicalName("_golem_fulltext_" + string(normalizationExtensionID) + "_fts"))
	if err := copied.GetContext(ctx, &matches, "SELECT count(*) FROM "+fts+" WHERE "+fts+" MATCH 'alpha'"); err != nil || matches != 1 {
		t.Fatalf("copied FTS matches=%d error=%v", matches, err)
	}
	var children int
	if err := copied.GetContext(ctx, &children, "SELECT count(*) FROM children WHERE item_id=1"); err != nil || children != 1 {
		t.Fatalf("copied relation rows=%d error=%v", children, err)
	}
}

func TestSQLiteSnapshotPreservesTwoTableBoundaryDuringUncommittedWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source, schema, manifest := sqliteSnapshotFixture(t)
	source.SetMaxOpenConns(2)
	source.SetMaxIdleConns(2)
	var journal string
	if err := source.GetContext(ctx, &journal, `PRAGMA journal_mode`); err != nil || journal != "wal" {
		t.Fatalf("fixture must exercise concurrent WAL readers: mode=%q error=%v", journal, err)
	}
	if _, err := source.ExecContext(ctx, `INSERT INTO items(id,name) VALUES(3,'other parent')`); err != nil {
		t.Fatal(err)
	}
	writer, err := source.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(ctx, `UPDATE items SET name='committed beta' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	type pair struct {
		Name   string
		Parent int
	}
	const pairSQL = `SELECT i.name,c.item_id AS parent FROM items i CROSS JOIN children c WHERE i.id=1 AND c.id=2`
	var pending pair
	if err := writer.GetContext(ctx, &pending, pairSQL); err != nil || pending != (pair{"committed beta", 1}) {
		t.Fatalf("writer did not reach the partial two-table state: pair=%+v error=%v", pending, err)
	}
	// The writer retains the first real connection and remains uncommitted
	// throughout Snapshot. Copying must use the second WAL connection without
	// observing the first write or waiting for this transaction to commit.
	if status := source.Stats(); status.MaxOpenConnections != 2 || status.InUse != 1 {
		t.Fatalf("writer connection ownership differs from the fixture: %+v", status)
	}
	directory := t.TempDir()
	destination := filepath.Join(directory, "snapshot.db")
	if err := New().Snapshot(ctx, source, schema, manifest, destination); err != nil {
		t.Fatalf("snapshot while writer holds partial transaction: %v", err)
	}
	copyDatabase, err := openSQLiteSnapshotVerification(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDatabase.Close()
	var copied pair
	if err := copyDatabase.GetContext(ctx, &copied, pairSQL); err != nil || copied != (pair{"snapshot alpha", 1}) {
		t.Fatalf("snapshot crossed the uncommitted two-table boundary: pair=%+v error=%v", copied, err)
	}
	if _, err := writer.ExecContext(ctx, `UPDATE children SET item_id=3 WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	var current pair
	if err := source.GetContext(ctx, &current, pairSQL); err != nil || current != (pair{"committed beta", 3}) {
		t.Fatalf("source did not commit the complete pair: pair=%+v error=%v", current, err)
	}
	if err := copyDatabase.GetContext(ctx, &copied, pairSQL); err != nil || copied != (pair{"snapshot alpha", 1}) {
		t.Fatalf("completed copy moved after the writer committed: pair=%+v error=%v", copied, err)
	}
	if err := source.GetContext(ctx, &journal, `PRAGMA journal_mode`); err != nil || journal != "wal" {
		t.Fatalf("snapshot changed the source journal profile: mode=%q error=%v", journal, err)
	}
	if err := copyDatabase.Close(); err != nil {
		t.Fatal(err)
	}
	assertSQLiteSnapshotDirectory(t, directory, "snapshot.db")
}

func TestSQLiteSnapshotRefusesInvalidArtifactAndCleansOnlyStaging(t *testing.T) {
	for _, failure := range []string{"ledger", "catalog", "foreign_key"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			source, schema, manifest := sqliteSnapshotFixture(t)
			switch failure {
			case "ledger":
				if _, err := source.ExecContext(ctx, "DELETE FROM _golem_migrations"); err != nil {
					t.Fatal(err)
				}
			case "catalog":
				if _, err := source.ExecContext(ctx, "ALTER TABLE items ADD COLUMN unreviewed TEXT"); err != nil {
					t.Fatal(err)
				}
			case "foreign_key":
				connection, err := source.Connx(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_, err = connection.ExecContext(ctx, "PRAGMA foreign_keys=OFF; INSERT INTO children(id,item_id) VALUES(3,999); PRAGMA foreign_keys=ON")
				closeErr := connection.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("fixture error=%v close=%v", err, closeErr)
				}
			}
			directory := t.TempDir()
			if err := New().Snapshot(ctx, source, schema, manifest, filepath.Join(directory, "snapshot.db")); err == nil || errors.Is(err, ErrSnapshotPublished) {
				t.Fatalf("invalid artifact publication error=%v", err)
			}
			assertSQLiteSnapshotDirectory(t, directory)
			var one int
			if err := source.GetContext(ctx, &one, "SELECT 1"); err != nil || one != 1 {
				t.Fatalf("source unusable after refusal: %v", err)
			}
		})
	}
}

func TestSQLiteSnapshotNeverOverwritesDestination(t *testing.T) {
	source, schema, manifest := sqliteSnapshotFixture(t)
	for _, kind := range []string{"file", "empty", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			destination := filepath.Join(directory, "destination")
			canary := []byte("existing-private-destination")
			switch kind {
			case "file":
				if err := os.WriteFile(destination, canary, 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(destination, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.WriteFile(filepath.Join(directory, "target"), canary, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target", destination); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(destination)
			if err != nil {
				t.Fatal(err)
			}
			if err := New().Snapshot(context.Background(), source, schema, manifest, destination); err == nil {
				t.Fatal("existing destination accepted")
			}
			after, err := os.Lstat(destination)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("destination replaced: %v", err)
			}
			if kind == "file" || kind == "symlink" {
				content, err := os.ReadFile(destination)
				if err != nil || !reflect.DeepEqual(content, canary) {
					t.Fatalf("existing content changed: %v", err)
				}
			}
			if kind == "symlink" {
				assertSQLiteSnapshotDirectory(t, directory, "destination", "target")
			} else {
				assertSQLiteSnapshotDirectory(t, directory, "destination")
			}
		})
	}
}

func TestSQLiteSnapshotRefusesOccupiedDestinationSidecarNamespace(t *testing.T) {
	source, schema, manifest := sqliteSnapshotFixture(t)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		for _, kind := range []string{"empty", "nonempty", "symlink", "directory"} {
			t.Run(suffix+"/"+kind, func(t *testing.T) {
				directory := t.TempDir()
				destination := filepath.Join(directory, "snapshot.db")
				sidecar := destination + suffix
				var contents []byte
				var err error
				switch kind {
				case "empty", "nonempty":
					if kind == "nonempty" {
						contents = []byte("existing owner data")
					}
					err = os.WriteFile(sidecar, contents, 0o600)
				case "symlink":
					// A dangling symlink still occupies somebody else's namespace.
					err = os.Symlink("untouched-owner-target", sidecar)
				case "directory":
					err = os.Mkdir(sidecar, 0o700)
				}
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.Lstat(sidecar)
				if err != nil {
					t.Fatal(err)
				}
				snapshotErr := New().Snapshot(context.Background(), source, schema, manifest, destination)
				after, err := os.Lstat(sidecar)
				if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
					t.Fatalf("existing sidecar replaced or removed: %v", err)
				}
				if kind == "empty" || kind == "nonempty" {
					got, err := os.ReadFile(sidecar)
					if err != nil || string(got) != string(contents) {
						t.Fatalf("existing sidecar content changed: %q %v", got, err)
					}
				}
				if kind == "symlink" {
					got, err := os.Readlink(sidecar)
					if err != nil || got != "untouched-owner-target" {
						t.Fatalf("existing sidecar link changed: %q %v", got, err)
					}
				}
				if snapshotErr == nil || errors.Is(snapshotErr, ErrSnapshotPublished) {
					t.Errorf("occupied destination namespace published: %v", snapshotErr)
				}
				assertSQLiteSnapshotDirectory(t, directory, "snapshot.db"+suffix)
			})
		}
	}
}

func TestSQLiteSnapshotRefusesDestinationSidecarCreatedDuringCopyWait(t *testing.T) {
	source, schema, manifest := sqliteSnapshotFixture(t)
	source.SetMaxOpenConns(1)
	source.SetMaxIdleConns(1)
	holder, err := source.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	directory := t.TempDir()
	destination := filepath.Join(directory, "snapshot.db")
	before := source.Stats().WaitCount
	result := make(chan error, 1)
	go func() { result <- New().Snapshot(ctx, source, schema, manifest, destination) }()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for source.Stats().WaitCount == before {
		select {
		case err := <-result:
			t.Fatalf("snapshot did not wait after initial destination admission: %v", err)
		case <-ctx.Done():
			t.Fatal("snapshot did not enter the pool wait")
		case <-tick.C:
		}
	}
	sidecar := destination + "-wal"
	if err := os.WriteFile(sidecar, []byte("concurrent owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || errors.Is(err, ErrSnapshotPublished) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("late sidecar must refuse before publication: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("snapshot did not finish after releasing the source connection")
	}
	contents, err := os.ReadFile(sidecar)
	if err != nil || string(contents) != "concurrent owner" {
		t.Fatalf("late sidecar was changed: %q %v", contents, err)
	}
	assertSQLiteSnapshotDirectory(t, directory, "snapshot.db-wal")
}

func TestSQLiteSnapshotQueryOnlyVerificationRejectsTruncatedArtifact(t *testing.T) {
	source, schema, manifest := sqliteSnapshotFixture(t)
	path := filepath.Join(t.TempDir(), "truncated.db")
	if err := copySQLiteSnapshot(context.Background(), source, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 100); err != nil {
		t.Fatal(err)
	}
	if err := New().verifySnapshot(context.Background(), path, schema, manifest); err == nil {
		t.Fatal("truncated artifact accepted")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 100 {
		t.Fatalf("query-only validation changed private artifact: info=%v error=%v", info, err)
	}
}

func TestSQLiteSnapshotCanceledPoolWaitCleansStagingAndReleasesOwnership(t *testing.T) {
	source, schema, manifest := sqliteSnapshotFixture(t)
	source.SetMaxOpenConns(1)
	source.SetMaxIdleConns(1)
	holder, err := source.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	before := source.Stats().WaitCount
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directory := t.TempDir()
	result := make(chan error, 1)
	go func() {
		result <- New().Snapshot(ctx, source, schema, manifest, filepath.Join(directory, "snapshot.db"))
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for source.Stats().WaitCount == before {
		select {
		case err := <-result:
			t.Fatalf("snapshot did not wait for its source connection: %v", err)
		case <-deadline.C:
			cancel()
			<-result
			t.Fatal("snapshot did not enter the pool wait")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pool wait error=%v", err)
		}
	case <-deadline.C:
		t.Fatal("snapshot did not finish after cancellation")
	}
	assertSQLiteSnapshotDirectory(t, directory)
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	var one int
	if err := source.Get(&one, "SELECT 1"); err != nil || one != 1 {
		t.Fatalf("source unavailable after canceled snapshot: %v", err)
	}
}

func TestSQLiteSnapshotPublicationReportsPointOfNoReturn(t *testing.T) {
	directory := t.TempDir()
	stage, destination := filepath.Join(directory, "stage"), filepath.Join(directory, "destination")
	if err := os.WriteFile(stage, []byte("verified-and-synced-copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	published, err := publishSQLiteSnapshot(stage, destination, parent)
	if !published || err == nil {
		t.Fatalf("closed parent sync: published=%v error=%v", published, err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("published artifact disappeared: %v", err)
	}
	parent, err = os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	published, err = publishSQLiteSnapshot(stage, destination, parent)
	if published || err == nil {
		t.Fatalf("concurrent destination collision: published=%v error=%v", published, err)
	}
}

func assertSQLiteSnapshotDirectory(t *testing.T, directory string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if len(got) != len(names) {
		t.Fatalf("snapshot directory=%v want=%v", got, names)
	}
	for index := range got {
		if got[index] != names[index] {
			t.Fatalf("snapshot directory=%v want=%v", got, names)
		}
	}
}
