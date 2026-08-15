package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
)

var p4OracleNamespaceSequence atomic.Uint64

type mutationProviderAcceptanceFixture struct {
	fixture     mutationResultFixture
	provider    golem.Provider
	posts       string
	outbox      string
	guard       string
	placeholder func(int) string
}

func runMutationProviderAcceptanceProfiles(t *testing.T, operation func(*testing.T, mutationProviderAcceptanceFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		operation(t, mutationProviderAcceptanceFixture{
			fixture: newMutationResultFixture(t), provider: golem.SQLite, posts: `"posts"`, outbox: `"_golem_outbox"`, guard: `"_golem_upsert_guard"`,
			placeholder: func(int) string { return "?" },
		})
	})
	for _, profile := range []struct{ name, namespace, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for mutation provider acceptance", profile.env)
			}
			fixture, applicationNamespace, systemNamespace := newPostgreSQLMutationOracleFixture(t, dsn, profile.namespace)
			operation(t, mutationProviderAcceptanceFixture{
				fixture: fixture, provider: golem.PostgreSQL, posts: oracleQualified(applicationNamespace, "posts"), outbox: oracleQualified(systemNamespace, "_golem_outbox"),
				placeholder: func(index int) string { return fmt.Sprintf("$%d", index) },
			})
		})
	}
}

// TestP4IndependentMutationOracleSQLite deliberately computes expectations
// with database/sql only. The public generated-facing mutation API is the
// system under test; no planner, renderer, runtime decoder, or policy evaluator
// is used to derive the expected database state.
func TestP4IndependentMutationOracleSQLite(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	system := fixture.app.System()
	raw := fixture.app.database.DB

	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(91, golem.UUID{15: 1}, "created")); err != nil {
		t.Fatal(err)
	}
	assertOraclePost(t, raw, 91, "created")
	if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(91), fixture.updateTitle("updated")); err != nil {
		t.Fatal(err)
	}
	assertOraclePost(t, raw, 91, "updated")
	// Persisted no-op updates remain successful and still have one exact fact.
	if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(91), fixture.updateTitle("updated")); err != nil {
		t.Fatal(err)
	}

	if _, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(92), fixture.createPost(92, golem.UUID{15: 1}, "upsert-created"), fixture.updateTitle("wrong")); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(92), fixture.createPost(92, golem.UUID{15: 1}, "wrong"), fixture.updateTitle("upsert-updated")); err != nil {
		t.Fatal(err)
	}
	assertOraclePost(t, raw, 92, "upsert-updated")

	for _, id := range []byte{93, 94, 95} {
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "batch")); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := SystemUpdateMany(ctx, system, fixture.postDescriptor, fixture.title.Eq("batch"), fixture.updateManyTitle("batched")); err != nil || count != 3 {
		t.Fatalf("updateMany count=%d err=%v", count, err)
	}
	if count, err := SystemDeleteMany(ctx, system, fixture.postDescriptor, fixture.title.Eq("batched")); err != nil || count != 3 {
		t.Fatalf("deleteMany count=%d err=%v", count, err)
	}
	assertOracleCount(t, raw, `SELECT COUNT(*) FROM "posts" WHERE "title" IN ('batch','batched')`, 0)

	if _, err := SystemDelete(ctx, system, fixture.postDescriptor, fixture.target(91)); err != nil {
		t.Fatal(err)
	}
	assertOracleCount(t, raw, `SELECT COUNT(*) FROM "posts" WHERE "id"=?`, 0, mutationResultUUIDText(91))

	// Independently inspect the stable envelope prefix/version and relational
	// action/ordinal contract without calling the production fact decoder.
	rows, err := raw.QueryContext(ctx, `SELECT "action", "transaction_ordinal", "metadata" FROM "_golem_outbox" ORDER BY "recorded_at", "event_id"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	facts := 0
	for rows.Next() {
		var action string
		var ordinal int64
		var metadata []byte
		if err := rows.Scan(&action, &ordinal, &metadata); err != nil {
			t.Fatal(err)
		}
		if action != "created" && action != "updated" && action != "deleted" || ordinal < 1 || !independentFactHeaderValid(metadata) {
			t.Fatalf("invalid independent fact action=%q ordinal=%d metadata=%x", action, ordinal, metadata)
		}
		facts++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// 1 create + 2 updates + 2 upsert branches + 3 creates + 3 batch updates
	// + 3 batch deletes + 1 scalar delete.
	if facts != 15 {
		t.Fatalf("outbox facts=%d want=15", facts)
	}

	var callbacks atomic.Int64
	sentinel := errors.New("oracle rollback")
	epoch := system.executor.invalidationEpoch()
	err = SystemTransaction(ctx, system, func(tx *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		callbacks.Add(1)
		if _, err := SystemTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(96, golem.UUID{15: 1}, "rolled-back")); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || callbacks.Load() != 1 || system.executor.invalidationEpoch() != epoch {
		t.Fatalf("transaction rollback err=%v callbacks=%d epoch=%d", err, callbacks.Load(), system.executor.invalidationEpoch())
	}
	assertOracleCount(t, raw, `SELECT COUNT(*) FROM "posts" WHERE "id"=?`, 0, mutationResultUUIDText(96))
}

func TestP4IndependentMutationOraclePostgreSQLProfiles(t *testing.T) {
	profiles := []struct {
		name string
		env  string
	}{
		{name: "c", env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	for _, profile := range profiles {
		dsn := strings.TrimSpace(os.Getenv(profile.env))
		if dsn == "" {
			t.Skipf("P4 PostgreSQL oracle evidence is incomplete: %s is required", profile.env)
		}
		t.Run(profile.name, func(t *testing.T) {
			fixture, applicationNamespace, systemNamespace := newPostgreSQLMutationOracleFixture(t, dsn, profile.name)
			ctx := context.Background()
			system := fixture.app.System()
			posts := oracleQualified(applicationNamespace, "posts")
			outbox := oracleQualified(systemNamespace, "_golem_outbox")

			if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(101, golem.UUID{15: 1}, "I-ı-é-created")); err != nil {
				t.Fatal(err)
			}
			if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(101), fixture.updateTitle("I-ı-é-updated")); err != nil {
				t.Fatal(err)
			}
			if _, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(102), fixture.createPost(102, golem.UUID{15: 1}, "upsert-created"), fixture.updateTitle("wrong")); err != nil {
				t.Fatal(err)
			}
			if _, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(102), fixture.createPost(102, golem.UUID{15: 1}, "wrong"), fixture.updateTitle("upsert-updated")); err != nil {
				t.Fatal(err)
			}
			var title string
			if err := fixture.app.database.DB.QueryRowContext(ctx, `SELECT "title" FROM `+posts+` WHERE "id"=$1`, mutationResultUUIDText(101)).Scan(&title); err != nil || title != "I-ı-é-updated" {
				t.Fatalf("raw PostgreSQL title=%q err=%v", title, err)
			}
			if err := fixture.app.database.DB.QueryRowContext(ctx, `SELECT "title" FROM `+posts+` WHERE "id"=$1`, mutationResultUUIDText(102)).Scan(&title); err != nil || title != "upsert-updated" {
				t.Fatalf("raw PostgreSQL upsert title=%q err=%v", title, err)
			}

			for _, id := range []byte{103, 104} {
				if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "batch-pg")); err != nil {
					t.Fatal(err)
				}
			}
			if count, err := SystemUpdateMany(ctx, system, fixture.postDescriptor, fixture.title.Eq("batch-pg"), fixture.updateManyTitle("batch-pg-updated")); err != nil || count != 2 {
				t.Fatalf("PostgreSQL updateMany count=%d err=%v", count, err)
			}
			if count, err := SystemDeleteMany(ctx, system, fixture.postDescriptor, fixture.title.Eq("batch-pg-updated")); err != nil || count != 2 {
				t.Fatalf("PostgreSQL deleteMany count=%d err=%v", count, err)
			}

			var factCount int
			if err := fixture.app.database.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+outbox).Scan(&factCount); err != nil || factCount != 10 {
				t.Fatalf("PostgreSQL facts=%d want=10 err=%v", factCount, err)
			}
			rows, err := fixture.app.database.DB.QueryContext(ctx, `SELECT "action", "transaction_ordinal", "metadata" FROM `+outbox)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var action string
				var ordinal int64
				var metadata []byte
				if err := rows.Scan(&action, &ordinal, &metadata); err != nil {
					t.Fatal(err)
				}
				if ordinal < 1 || !independentFactHeaderValid(metadata) {
					t.Fatalf("PostgreSQL fact action=%q ordinal=%d metadata=%x", action, ordinal, metadata)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}

			var wait sync.WaitGroup
			concurrentErrors := make([]error, 6)
			for worker := range concurrentErrors {
				wait.Add(1)
				go func(worker int) {
					defer wait.Done()
					_, concurrentErrors[worker] = SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(105), fixture.createPost(105, golem.UUID{15: 1}, fmt.Sprintf("create-%d", worker)), fixture.updateTitle(fmt.Sprintf("update-%d", worker)))
				}(worker)
			}
			wait.Wait()
			successes := 0
			for _, err := range concurrentErrors {
				if err == nil {
					successes++
					continue
				}
				var public *golem.Error
				if !errors.As(err, &public) || public.Code != golem.CodeConflict {
					t.Fatalf("concurrent upsert error=%v", err)
				}
			}
			if successes == 0 {
				t.Fatal("concurrent upsert produced no committed attempt")
			}
			var concurrentRows int
			if err := fixture.app.database.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+posts+` WHERE "id"=$1`, mutationResultUUIDText(105)).Scan(&concurrentRows); err != nil || concurrentRows != 1 {
				t.Fatalf("concurrent selector rows=%d err=%v", concurrentRows, err)
			}
		})
	}
}

// independentFactHeaderValid intentionally does not call the production fact
// decoder. It understands the persisted V1/V2 prefix well enough to prove the
// exact version/codec pairing and that V2 carries a non-zero event-schema
// digest, while retaining historical V1 acceptance for upgrade coverage.
func independentFactHeaderValid(metadata []byte) bool {
	if len(metadata) < 15 || !bytes.Equal(metadata[:9], []byte("GOLEMFACT")) {
		return false
	}
	version := binary.BigEndian.Uint16(metadata[9:11])
	codecLength := int(binary.BigEndian.Uint32(metadata[11:15]))
	if codecLength < 1 || codecLength > len(metadata)-15 {
		return false
	}
	offset := 15
	codec := string(metadata[offset : offset+codecLength])
	offset += codecLength
	if version == 1 && codec != "golem.fact.v1" || version == 2 && codec != "golem.fact.v2" || version != 1 && version != 2 {
		return false
	}
	// event ID + generation digest are common to both formats.
	if len(metadata)-offset < 16+32 {
		return false
	}
	offset += 16 + 32
	if version == 1 {
		return true
	}
	if len(metadata)-offset < 32 {
		return false
	}
	var nonZero bool
	for _, value := range metadata[offset : offset+32] {
		nonZero = nonZero || value != 0
	}
	return nonZero
}

func TestRollbackDoesNotPublishInvalidation(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		system := fixture.app.System()
		epoch := system.executor.invalidationEpoch()
		sentinel := errors.New("rollback invalidation oracle")
		err := SystemTransaction(ctx, system, func(tx *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, err := SystemTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(111, golem.UUID{15: 1}, "rollback")); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) || system.executor.invalidationEpoch() != epoch {
			t.Fatalf("rollback err=%v epoch=%d want=%d", err, system.executor.invalidationEpoch(), epoch)
		}
		var count int
		if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM `+profile.posts+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(111)); err != nil || count != 0 {
			t.Fatalf("rolled-back rows=%d err=%v", count, err)
		}
		if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM `+profile.outbox); err != nil || count != 0 {
			t.Fatalf("rolled-back facts=%d err=%v", count, err)
		}
	})
}

func TestUpsertRunsAndRecordsOnlyCommittedBranch(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		system := fixture.app.System()
		if _, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(112), fixture.createPost(112, golem.UUID{15: 1}, "created-branch"), fixture.updateTitle("wrong")); err != nil {
			t.Fatal(err)
		}
		if _, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(112), fixture.createPost(112, golem.UUID{15: 1}, "wrong"), fixture.updateTitle("updated-branch")); err != nil {
			t.Fatal(err)
		}
		rows, err := fixture.app.database.DB.Query(`SELECT "action" FROM ` + profile.outbox + ` ORDER BY "recorded_at", "event_id"`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var actions []string
		for rows.Next() {
			var action string
			if err := rows.Scan(&action); err != nil {
				t.Fatal(err)
			}
			actions = append(actions, action)
		}
		if len(actions) != 2 || actions[0] != "created" || actions[1] != "updated" {
			t.Fatalf("committed upsert branch facts=%v", actions)
		}
		var title string
		if err := fixture.app.database.GetContext(ctx, &title, `SELECT "title" FROM `+profile.posts+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(112)); err != nil || title != "updated-branch" {
			t.Fatalf("committed upsert title=%q err=%v", title, err)
		}
	})
}

func TestTransactionCommitClearsOnceAcrossEveryWriteEntryPoint(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		system := fixture.app.System()
		epoch := system.executor.invalidationEpoch()
		err := SystemTransaction(ctx, system, func(tx *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, err := SystemTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(113, golem.UUID{15: 1}, "scalar")); err != nil {
				return err
			}
			if _, err := SystemTxUpsert(ctx, tx, fixture.postDescriptor, fixture.target(114), fixture.createPost(114, golem.UUID{15: 1}, "upsert"), fixture.updateTitle("wrong")); err != nil {
				return err
			}
			if _, err := SystemTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(115, golem.UUID{15: 1}, "batch")); err != nil {
				return err
			}
			count, err := SystemTxUpdateMany(ctx, tx, fixture.postDescriptor, fixture.title.Eq("batch"), fixture.updateManyTitle("batched"))
			if err != nil || count != 1 {
				return errors.Join(err, fmt.Errorf("batch count=%d", count))
			}
			if system.executor.invalidationEpoch() != epoch {
				return fmt.Errorf("invalidation published before commit")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if system.executor.invalidationEpoch() != epoch+1 {
			t.Fatalf("commit epoch=%d want=%d", system.executor.invalidationEpoch(), epoch+1)
		}
	})
}

func TestUnauthorizedAndMissingMutationTargetsShareOnePublicErrorAndLeaveRowsUnchanged(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, runInvisibleMissingMutationOracle)
}

func runInvisibleMissingMutationOracle(t *testing.T, profile mutationProviderAcceptanceFixture) {
	t.Helper()
	ctx := context.Background()
	fixture := profile.fixture
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(121, golem.UUID{15: 2}, "invisible")); err != nil {
		t.Fatal(err)
	}
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		return rules.Freeze(fixture.schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanUpdate(fixture.authorID.Eq(golem.UUID{15: 1}))
		return rules.Freeze(fixture.schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, nil))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		Database: p8RuntimeTestDatabase(fixture.app.database, profile.provider), Bundle: fixture.schema.Bundle,
		Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
			return mutationResultActor{}, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	_, invisible := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(121), fixture.updateTitle("forbidden"))
	_, missing := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(122), fixture.updateTitle("missing"))
	var invisiblePublic, missingPublic *golem.Error
	if !errors.As(invisible, &invisiblePublic) || !errors.As(missing, &missingPublic) || invisiblePublic.Code != missingPublic.Code || invisiblePublic.Error() != missingPublic.Error() {
		t.Fatalf("invisible=%#v %v missing=%#v %v", invisiblePublic, invisible, missingPublic, missing)
	}
	var title string
	if err := fixture.app.database.GetContext(ctx, &title, `SELECT "title" FROM `+profile.posts+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(121)); err != nil || title != "invisible" {
		t.Fatalf("locked invisible row title=%q err=%v", title, err)
	}
}

func newPostgreSQLMutationOracleFixture(t *testing.T, dsn, profile string) (mutationResultFixture, string, string) {
	return newPostgreSQLMutationOracleFixtureFromBase(t, dsn, profile, newMutationResultFixture(t))
}

func newPostgreSQLMutationOracleFixtureFromBase(t *testing.T, dsn, profile string, base mutationResultFixture) (mutationResultFixture, string, string) {
	t.Helper()
	sequence := p4OracleNamespaceSequence.Add(1)
	applicationNamespace := fmt.Sprintf("golem_p4_oracle_%s_%d_%d", profile, os.Getpid(), sequence)
	systemNamespace := fmt.Sprintf("golem_p4_system_%s_%d_%d", profile, os.Getpid(), sequence)
	schemaFixture := schematest.NewSubscribedIndexedPostgreSQLNamespaces(t, physical.PhysicalName(applicationNamespace), physical.PhysicalName(systemNamespace))
	provider := postgresprovider.New()
	database, _, err := provider.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + oracleQuote(applicationNamespace) + ` CASCADE`)
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + oracleQuote(systemNamespace) + ` CASCADE`)
		_ = database.Close()
	})
	if err := provider.ApplyInitial(context.Background(), database, schemaFixture.PostgreSQL); err != nil {
		t.Fatal(err)
	}
	for _, user := range [][2]string{{mutationResultUUIDText(1), "alice"}, {mutationResultUUIDText(2), "bob"}} {
		if _, err := database.Exec(`INSERT INTO `+oracleQualified(applicationNamespace, "users")+` ("id","name") VALUES ($1,$2)`, user[0], user[1]); err != nil {
			t.Fatal(err)
		}
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		Database: p8RuntimeTestDatabase(database, golem.PostgreSQL), Bundle: schemaFixture.Bundle,
		Bindings: base.app.bindings, Descriptors: base.app.descriptors,
		ResolvePrincipal: base.app.resolvePrincipal, SnapshotActor: base.app.snapshotActor,
	}))
	if err != nil {
		t.Fatal(err)
	}
	base.app, base.schema = app, schemaFixture
	return base, applicationNamespace, systemNamespace
}

func oracleQualified(namespace, table string) string {
	return oracleQuote(namespace) + "." + oracleQuote(table)
}

func oracleQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func assertOraclePost(t *testing.T, database *sql.DB, id byte, wantTitle string) {
	t.Helper()
	var title string
	if err := database.QueryRow(`SELECT "title" FROM "posts" WHERE "id"=?`, mutationResultUUIDText(id)).Scan(&title); err != nil || title != wantTitle {
		t.Fatalf("post %d title=%q want=%q err=%v", id, title, wantTitle, err)
	}
}

func assertOracleCount(t *testing.T, database *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var count int
	if err := database.QueryRow(query, args...).Scan(&count); err != nil || count != want {
		t.Fatalf("oracle count=%d want=%d err=%v", count, want, err)
	}
}
