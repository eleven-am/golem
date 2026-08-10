package runtime

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/gentest"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

type runtimeValuesUser struct{}

func TestRuntimeValuesFreezeApplicationDefaultsAndUpdatedField(t *testing.T) {
	registry, model, id, handle, updated := runtimeValuesRegistry(t)
	handleField := golem.GeneratedTextField[runtimeValuesUser, string](handle)
	create := golem.GeneratedCreateInput(model, golem.GeneratedCreateFieldValue(model, handleField, "alice"))
	frozen, err := golem.RuntimeFreezeCreateInput(create)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := mutationbind.CreateInput(frozen, registry)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 6, 12, 34, 56, 987_654_321, time.FixedZone("test", 3600))
	entropy := bytes.NewReader([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	values := newMutationRuntimeValuesFrom(now, entropy)
	first, err := values.apply(bound, registry)
	if err != nil {
		t.Fatal(err)
	}
	second, err := values.apply(bound, registry)
	if err != nil {
		t.Fatal(err)
	}

	firstByField := runtimeOperationsByField(first)
	secondByField := runtimeOperationsByField(second)
	if len(firstByField) != 3 {
		t.Fatalf("create operation count = %d, want 3", len(firstByField))
	}
	if firstByField[policyir.FieldID(handle)].RuntimeOwned() {
		t.Fatal("explicit handle became runtime-owned")
	}
	for _, field := range []golem.FieldID{id, updated} {
		operation, ok := firstByField[policyir.FieldID(field)]
		if !ok || !operation.RuntimeOwned() {
			t.Fatalf("field %x was not materialized as runtime-owned", field)
		}
	}
	firstUUID, _ := runtimeOperationValue(t, firstByField[policyir.FieldID(id)]).UUID()
	secondUUID, _ := runtimeOperationValue(t, secondByField[policyir.FieldID(id)]).UUID()
	if firstUUID != secondUUID || firstUUID[6]>>4 != 4 || firstUUID[8]>>6 != 2 {
		t.Fatalf("frozen UUID mismatch or invalid RFC 4122 bits: %x %x", firstUUID, secondUUID)
	}
	seconds, nanos, ok := runtimeOperationValue(t, firstByField[policyir.FieldID(updated)]).DateTime()
	if !ok {
		t.Fatal("updated value is not DateTime")
	}
	want := now.UTC().Truncate(time.Microsecond)
	if seconds != want.Unix() || nanos != uint32(want.Nanosecond()) {
		t.Fatalf("updated value = (%d,%d), want (%d,%d)", seconds, nanos, want.Unix(), want.Nanosecond())
	}
}

func TestRuntimeValuesRefreshUpdatedFieldPerLogicalMutation(t *testing.T) {
	registry, model, _, handle, updated := runtimeValuesRegistry(t)
	frozen, err := golem.RuntimeMutationInputFromFields(model, []golem.RuntimeMutationFieldValue{{Field: handle, Operation: golem.MutationFieldSet, Value: "renamed", HasValue: true}})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := mutationbind.UpdateInput(frozen, registry)
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot := newMutationRuntimeValuesFrom(time.Unix(100, 123_456_789), bytes.NewReader(nil))
	secondSnapshot := newMutationRuntimeValuesFrom(time.Unix(101, 987_654_321), bytes.NewReader(nil))
	first, err := firstSnapshot.apply(bound, registry)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := firstSnapshot.apply(bound, registry)
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondSnapshot.apply(bound, registry)
	if err != nil {
		t.Fatal(err)
	}
	firstValue := runtimeOperationValue(t, runtimeOperationsByField(first)[policyir.FieldID(updated)])
	retryValue := runtimeOperationValue(t, runtimeOperationsByField(retry)[policyir.FieldID(updated)])
	secondValue := runtimeOperationValue(t, runtimeOperationsByField(second)[policyir.FieldID(updated)])
	firstSeconds, firstNanos, _ := firstValue.DateTime()
	retrySeconds, retryNanos, _ := retryValue.DateTime()
	secondSeconds, secondNanos, _ := secondValue.DateTime()
	if firstSeconds != retrySeconds || firstNanos != retryNanos {
		t.Fatal("one logical mutation changed its updated value during retry preparation")
	}
	if firstSeconds == secondSeconds && firstNanos == secondNanos {
		t.Fatal("a later logical mutation did not refresh its updated value")
	}
}

func TestRuntimeValuesGiveCreateManySlotsDistinctRetryStableUUIDs(t *testing.T) {
	registry, model, id, handle, _ := runtimeValuesRegistry(t)
	handleField := golem.GeneratedTextField[runtimeValuesUser, string](handle)
	freeze := func(name string) mutationbind.ScalarInput {
		t.Helper()
		input := golem.GeneratedCreateInput(model, golem.GeneratedCreateFieldValue(model, handleField, name))
		frozen, err := golem.RuntimeFreezeCreateInput(input)
		if err != nil {
			t.Fatal(err)
		}
		bound, err := mutationbind.CreateInput(frozen, registry)
		if err != nil {
			t.Fatal(err)
		}
		return bound
	}
	entropy := make([]byte, 32)
	for index := range entropy {
		entropy[index] = byte(index)
	}
	values := newMutationRuntimeValuesFrom(time.Unix(500, 0), bytes.NewReader(entropy))
	first, err := values.applyAt(freeze("first"), registry, 11)
	if err != nil {
		t.Fatal(err)
	}
	second, err := values.applyAt(freeze("second"), registry, 12)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := values.applyAt(freeze("first"), registry, 11)
	if err != nil {
		t.Fatal(err)
	}
	firstUUID, _ := runtimeOperationValue(t, runtimeOperationsByField(first)[policyir.FieldID(id)]).UUID()
	secondUUID, _ := runtimeOperationValue(t, runtimeOperationsByField(second)[policyir.FieldID(id)]).UUID()
	retryUUID, _ := runtimeOperationValue(t, runtimeOperationsByField(retry)[policyir.FieldID(id)]).UUID()
	if firstUUID == secondUUID {
		t.Fatal("distinct create-many slots reused one UUID")
	}
	if firstUUID != retryUUID {
		t.Fatal("the same create-many slot changed UUID across retry preparation")
	}
}

func TestSQLiteNestedCreateManyPersistsDistinctApplicationUUIDDefaults(t *testing.T) {
	schemaFixture := schematest.NewSubscribedGraphRuntimeDefaults(t)
	var before atomic.Int64
	hook := golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schemaFixture.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationPost]) error {
		before.Add(1)
		return nil
	})
	fixture := newGraphMutationFixtureWithHooks(t, schemaFixture, golem.ModelID{}, []golem.HookBinding[graphMutationActor]{hook})
	assertNestedCreateManyRuntimeUUIDs(t, fixture)
	if before.Load() != 2 {
		t.Fatalf("unchanged nested BeforeCreate calls=%d, want 2", before.Load())
	}
}

func TestPostgreSQLNestedCreateManyPersistsDistinctApplicationUUIDDefaults(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			namespace := physical.PhysicalName(fmt.Sprintf("golem_runtime_nested_%s_%d_%d", profile.name, os.Getpid(), sequence))
			systemNamespace := physical.PhysicalName(string(namespace) + "_system")
			schemaFixture := schematest.NewSubscribedGraphRuntimeDefaultsPostgreSQLNamespaces(t, namespace, systemNamespace)
			var before atomic.Int64
			hook := golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schemaFixture.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationPost]) error {
				before.Add(1)
				return nil
			})
			provider := postgresprovider.New()
			database, _, err := provider.Open(context.Background(), profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(namespace))+` CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(systemNamespace))+` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(context.Background(), database, schemaFixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			fixture := openGraphMutationFixtureWithHooks(t, database, golem.PostgreSQL, schemaFixture, golem.ModelID{}, []golem.HookBinding[graphMutationActor]{hook})
			assertNestedCreateManyRuntimeUUIDs(t, fixture)
			if before.Load() != 2 {
				t.Fatalf("unchanged nested BeforeCreate calls=%d, want 2", before.Load())
			}
		})
	}
}

func TestSQLiteNestedRuntimeUUIDsStayFrozenAcrossActualUpsertRetry(t *testing.T) {
	schemaFixture := schematest.NewSubscribedGraphRuntimeDefaults(t)
	var beforeRoot, beforeChildren, afterRoot, afterCommit atomic.Int64
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](schemaFixture.User, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationUser]) error {
			beforeRoot.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schemaFixture.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationPost]) error {
			beforeChildren.Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schemaFixture.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			afterRoot.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schemaFixture.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			afterCommit.Add(1)
			return nil
		}),
	}
	fixture := newGraphMutationFixtureWithHooks(t, schemaFixture, golem.ModelID{}, hooks)
	userID := golem.UUID{15: 220}
	post := func(title string) golem.CreateInput[graphMutationPost] {
		return golem.GeneratedCreateInput(schemaFixture.Post, golem.GeneratedCreateFieldValue(schemaFixture.Post, fixture.postTitle, title))
	}
	create := golem.GeneratedCreateInput(schemaFixture.User,
		golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userID, userID),
		golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userName, "retry-parent"),
		golem.GeneratedNestedCreateMany[graphMutationUser, graphMutationPost](schemaFixture.User, schemaFixture.UserPosts, schemaFixture.Authorship, schemaFixture.Post, post("retry-first"), post("retry-second")),
	)
	update := golem.GeneratedUpdateInput(schemaFixture.User, golem.GeneratedSetFieldValue(schemaFixture.User, fixture.userName, "unused"))
	target := golem.GeneratedUniqueSelectorValue[graphMutationUser](schemaFixture.User, schemaFixture.UserKey, golem.GeneratedSelectorComponent(schemaFixture.UserID, userID))
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextWithUpsertAttemptFinishFault(context.Background(), func(ordinal uint32) error {
		if ordinal == 1 {
			return &pgconn.PgError{Code: "40001", Message: "injected provider whole-attempt retry"}
		}
		return nil
	})
	if _, err := CallerUpsert(ctx, caller, fixture.userDescriptor, target, create, update); err != nil {
		t.Fatal(err)
	}
	if beforeRoot.Load() != 2 || beforeChildren.Load() != 4 || afterRoot.Load() != 2 || afterCommit.Load() != 1 {
		t.Fatalf("retry hooks root-before=%d child-before=%d after=%d commit=%d", beforeRoot.Load(), beforeChildren.Load(), afterRoot.Load(), afterCommit.Load())
	}
	var ids []string
	if err := fixture.app.database.Select(&ids, `SELECT "id" FROM `+nestedAcceptanceTable(fixture.app, schemaFixture.Post)+` ORDER BY "title"`); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("committed retry UUIDs=%q", ids)
	}
	assertGraphMutationRowsAndFacts(t, fixture, 1, 2, 0, 3)
}

func TestNestedMembershipRefreshesUpdatedFieldAcrossProvidersAndHookReplacement(t *testing.T) {
	run := func(t *testing.T, fixture graphMutationFixture, before, after, afterCommit *atomic.Int64) {
		t.Helper()
		ctx := context.Background()
		schemaFixture := fixture.schema
		user := func(id byte, name string) golem.CreateInput[graphMutationUser] {
			return golem.GeneratedCreateInput(schemaFixture.User,
				golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userID, golem.UUID{15: id}),
				golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userName, name),
			)
		}
		for _, value := range []struct {
			id   byte
			name string
		}{{1, "first"}, {2, "second"}} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, user(value.id, value.name)); err != nil {
				t.Fatal(err)
			}
		}
		authorID := golem.GeneratedEqualField[graphMutationPost, golem.UUID](schemaFixture.AuthorID)
		post := golem.GeneratedCreateInput(schemaFixture.Post,
			golem.GeneratedCreateFieldValue(schemaFixture.Post, fixture.postID, golem.UUID{15: 240}),
			golem.GeneratedCreateFieldValue(schemaFixture.Post, authorID, golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(schemaFixture.Post, fixture.postTitle, "membership-updated"),
		)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, post); err != nil {
			t.Fatal(err)
		}
		posts := nestedAcceptanceTable(fixture.app, schemaFixture.Post)
		setOld := func() {
			t.Helper()
			old := any(time.Unix(100, 0).UTC())
			if fixture.app.provider == policyir.ProviderSQLite {
				old = time.Unix(100, 0).UTC().UnixMicro()
			}
			query := fixture.app.database.Rebind(`UPDATE ` + posts + ` SET "updated_at"=? WHERE "id"=?`)
			if _, err := fixture.app.database.ExecContext(ctx, query, old, mutationResultUUIDText(240)); err != nil {
				t.Fatal(err)
			}
		}
		assertRefreshed := func() {
			t.Helper()
			query := fixture.app.database.Rebind(`SELECT "updated_at" FROM ` + posts + ` WHERE "id"=?`)
			if fixture.app.provider == policyir.ProviderSQLite {
				var got int64
				if err := fixture.app.database.GetContext(ctx, &got, query, mutationResultUUIDText(240)); err != nil || got == time.Unix(100, 0).UTC().UnixMicro() {
					t.Fatalf("SQLite membership updated_at=%d err=%v", got, err)
				}
				return
			}
			var got time.Time
			if err := fixture.app.database.GetContext(ctx, &got, query, mutationResultUUIDText(240)); err != nil || got.Equal(time.Unix(100, 0).UTC()) {
				t.Fatalf("PostgreSQL membership updated_at=%s err=%v", got, err)
			}
		}
		postTarget := golem.GeneratedUniqueSelectorValue[graphMutationPost](schemaFixture.Post, schemaFixture.PostKey,
			golem.GeneratedSelectorComponent(schemaFixture.PostID, golem.UUID{15: 240}))
		userTarget := func(id byte) golem.UniqueSelectorValue[graphMutationUser] {
			return golem.GeneratedUniqueSelectorValue[graphMutationUser](schemaFixture.User, schemaFixture.UserKey,
				golem.GeneratedSelectorComponent(schemaFixture.UserID, golem.UUID{15: id}))
		}
		setOld()
		sourceConnect := golem.GeneratedUpdateInput(schemaFixture.Post,
			golem.GeneratedNestedConnect[graphMutationPost, graphMutationUser](schemaFixture.Post, schemaFixture.PostAuthor, schemaFixture.Authorship, schemaFixture.User, userTarget(2)),
		)
		if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, postTarget, sourceConnect); err != nil {
			t.Fatal(err)
		}
		assertRefreshed()
		setOld()
		before.Store(0)
		after.Store(0)
		afterCommit.Store(0)
		if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
			t.Fatal(err)
		}
		inverseConnect := golem.GeneratedUpdateInput(schemaFixture.User,
			golem.GeneratedNestedConnect[graphMutationUser, graphMutationPost](schemaFixture.User, schemaFixture.UserPosts, schemaFixture.Authorship, schemaFixture.Post, postTarget),
		)
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget(1), inverseConnect); err != nil {
			t.Fatal(err)
		}
		if before.Load() != 1 || after.Load() != 1 || afterCommit.Load() != 1 {
			t.Fatalf("membership child update hooks before=%d after=%d afterCommit=%d", before.Load(), after.Load(), afterCommit.Load())
		}
		assertRefreshed()
		assertExactNestedUpsertFacts(t, fixture,
			[]policyir.ModelID{policyir.ModelID(schemaFixture.User), policyir.ModelID(schemaFixture.Post)},
			[]mutationir.FactAction{mutationir.FactUpdated, mutationir.FactUpdated})
	}

	newHooks := func(schemaFixture schematest.GraphFixture, before, after, afterCommit *atomic.Int64) []golem.HookBinding[graphMutationActor] {
		updated := golem.GeneratedEqualField[graphMutationPost, time.Time](schemaFixture.PostUpdatedAt)
		return []golem.HookBinding[graphMutationActor]{
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.UpdateHookRequest[graphMutationPost]](schemaFixture.Post, golem.HookUpdate, func(context.Context, *golem.UpdateHookRequest[graphMutationPost]) error { before.Add(1); return nil }),
			golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.UpdateHookResult[graphMutationPost]](schemaFixture.Post, golem.HookUpdate, func(_ context.Context, result golem.UpdateHookResult[graphMutationPost]) error {
				after.Add(1)
				if value, ok := golem.Value(result.After(), updated).Get(); !ok || value.IsZero() {
					return fmt.Errorf("updated field is absent from membership AfterUpdate")
				}
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.UpdateHookResult[graphMutationPost]](schemaFixture.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[graphMutationPost]) error { afterCommit.Add(1); return nil }),
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		schemaFixture := schematest.NewSubscribedGraphRuntimeDefaults(t)
		var before, after, afterCommit atomic.Int64
		fixture := newGraphMutationFixtureWithHooks(t, schemaFixture, golem.ModelID{}, newHooks(schemaFixture, &before, &after, &afterCommit))
		run(t, fixture, &before, &after, &afterCommit)
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			namespace := physical.PhysicalName(fmt.Sprintf("golem_runtime_membership_%s_%d_%d", profile.name, os.Getpid(), sequence))
			systemNamespace := physical.PhysicalName(string(namespace) + "_system")
			schemaFixture := schematest.NewSubscribedGraphRuntimeDefaultsPostgreSQLNamespaces(t, namespace, systemNamespace)
			provider := postgresprovider.New()
			database, _, err := provider.Open(context.Background(), profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(namespace))+` CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(systemNamespace))+` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(context.Background(), database, schemaFixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			var before, after, afterCommit atomic.Int64
			fixture := openGraphMutationFixtureWithHooks(t, database, golem.PostgreSQL, schemaFixture, golem.ModelID{}, newHooks(schemaFixture, &before, &after, &afterCommit))
			run(t, fixture, &before, &after, &afterCommit)
		})
	}
}

func assertNestedCreateManyRuntimeUUIDs(t *testing.T, fixture graphMutationFixture) {
	t.Helper()
	schemaFixture := fixture.schema
	post := func(title string) golem.CreateInput[graphMutationPost] {
		return golem.GeneratedCreateInput(schemaFixture.Post,
			golem.GeneratedCreateFieldValue(schemaFixture.Post, fixture.postTitle, title),
		)
	}
	input := golem.GeneratedCreateInput(schemaFixture.User,
		golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userName, "runtime-default-parent"),
		golem.GeneratedNestedCreateMany[graphMutationUser, graphMutationPost](schemaFixture.User, schemaFixture.UserPosts, schemaFixture.Authorship, schemaFixture.Post, post("first"), post("second")),
	)
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(context.Background(), caller, fixture.userDescriptor, input); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := fixture.app.database.Select(&ids, `SELECT "id" FROM `+nestedAcceptanceTable(fixture.app, schemaFixture.Post)+` ORDER BY "title"`); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("nested generated IDs=%q, want two distinct UUIDs", ids)
	}
	assertGraphMutationRowsAndFacts(t, fixture, 1, 2, 0, 3)
}

func TestSQLiteRuntimeValuesPersistOnCreateAndRefreshOnUpdate(t *testing.T) {
	ctx := context.Background()
	registry, model, id, handle, updated := runtimeValuesRegistry(t)
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:runtime_values_live?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.ExecContext(ctx, `CREATE TABLE "users" ("id" TEXT PRIMARY KEY, "handle" TEXT NOT NULL UNIQUE, "created_at" TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(registry.ModelFingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	runRuntimeValuesPersistence(t, ctx, database, policyir.ProviderSQLite, proof, registry, model, id, handle, updated)
}

func TestPostgreSQLRuntimeValuesPersistOnCreateAndRefreshOnUpdate(t *testing.T) {
	profiles := []struct{ name, env string }{{"postgresql-c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}}
	for _, profile := range profiles {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required", profile.env)
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			namespace := physical.PhysicalName(fmt.Sprintf("golem_runtime_values_%d_%d", os.Getpid(), sequence))
			fixture := newRuntimeValuesSchemaFixture(t, namespace)
			provider := postgresprovider.New()
			ctx := context.Background()
			database, _, err := provider.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(fixture.postgres.Namespace.Name))+` CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(fixture.postgres.System.Namespace.Name))+` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, fixture.postgres); err != nil {
				t.Fatal(err)
			}
			proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.registry.ModelFingerprint()))
			if err != nil {
				t.Fatal(err)
			}
			runRuntimeValuesPersistence(t, ctx, database, policyir.ProviderPostgreSQL, proof, fixture.registry, fixture.model, fixture.id, fixture.handle, fixture.updated)
		})
	}
}

func TestPreparedUpsertReusesRuntimeValuesAcrossEngineRetryPlan(t *testing.T) {
	registry, model, _, handle, updated := runtimeValuesRegistry(t)
	handleField := golem.GeneratedTextField[runtimeValuesUser, string](handle)
	create := golem.GeneratedCreateInput(model, golem.GeneratedCreateFieldValue(model, handleField, "upserted"))
	update := golem.GeneratedUpdateInput(model, golem.GeneratedSetFieldValue(model, handleField, "upserted"))
	frozenCreate, err := golem.RuntimeFreezeCreateInput(create)
	if err != nil {
		t.Fatal(err)
	}
	frozenUpdate, err := golem.RuntimeFreezeUpdateInput(update)
	if err != nil {
		t.Fatal(err)
	}
	modelFact, _ := registry.Model(model)
	identities := modelFact.Identities()
	if len(identities) < 2 {
		t.Fatal("handle unique identity is absent")
	}
	selector := golem.GeneratedUniqueSelectorValue[runtimeValuesUser](model, identities[1].KeyID(), golem.GeneratedSelectorComponent(handle, "upserted"))
	target, err := golem.RuntimeFreezeMutationTarget[runtimeValuesUser](selector)
	if err != nil {
		t.Fatal(err)
	}
	result, err := mutationir.NewImageRequirements(policyir.ModelID(model), []policyir.FieldID{policyir.FieldID(updated)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := normalizeMutationLimits(MutationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), "file:runtime_values_upsert?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	proof, err := provider.PolicyCapabilityProof(context.Background(), database, [32]byte(registry.ModelFingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	app := &App[struct{}, struct{}]{database: database, provider: policyir.ProviderSQLite, registry: registry, capabilities: proof, mutationLimits: limits}
	system := System[struct{}, struct{}]{app: app, executor: databaseExecution(database)}
	request := rootUpsertPrepareRequest{model: policyir.ModelID(model), target: target, create: frozenCreate, update: frozenUpdate, result: result, runtimeValues: newMutationRuntimeValuesFrom(time.Unix(1_900_000_000, 654_321_987), bytes.NewReader(make([]byte, 16)))}
	first, err := prepareSystemRootUpsert(system, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareSystemRootUpsert(system, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runtimeStaticBindings(first.create), runtimeStaticBindings(second.create)) || !reflect.DeepEqual(runtimeStaticBindings(first.update), runtimeStaticBindings(second.update)) {
		t.Fatal("engine-owned upsert retry planning changed frozen runtime bindings")
	}
}

func runtimeStaticBindings(program mutationsql.Program) []any {
	var result []any
	for _, statement := range program.Statements() {
		for _, binding := range statement.Bindings() {
			if value, ok := binding.StaticValue(); ok {
				result = append(result, value)
			}
		}
	}
	return result
}

func runRuntimeValuesPersistence(t *testing.T, ctx context.Context, database *sqlx.DB, provider policyir.Provider, proof policysql.CapabilityProof, registry *schema.Registry, model golem.ModelID, id, handle, updated golem.FieldID) {
	t.Helper()
	fields := []policyir.FieldID{policyir.FieldID(id), policyir.FieldID(handle), policyir.FieldID(updated)}
	result, err := mutationir.NewImageRequirements(policyir.ModelID(model), fields, nil)
	if err != nil {
		t.Fatal(err)
	}
	bounds, err := mutationir.NewStatementBounds(128, 16)
	if err != nil {
		t.Fatal(err)
	}
	render := func(operation mutationir.Operation, create, update *mutationbind.ScalarInput, target *mutationbind.BoundTarget) mutationsql.Program {
		t.Helper()
		plan, err := mutationplan.BuildRoot(mutationplan.RootRequest{Stance: mutationir.System, Operation: operation, Model: policyir.ModelID(model), Registry: registry, Create: create, Update: update, Target: target, Result: result, Retry: mutationir.NoRetry, Bounds: bounds})
		if err != nil {
			t.Fatal(err)
		}
		program, err := mutationsql.Render(plan, registry, provider, proof)
		if err != nil {
			t.Fatal(err)
		}
		return program
	}

	handleField := golem.GeneratedTextField[runtimeValuesUser, string](handle)
	createPublic := golem.GeneratedCreateInput(model, golem.GeneratedCreateFieldValue(model, handleField, "alice"))
	createFrozen, err := golem.RuntimeFreezeCreateInput(createPublic)
	if err != nil {
		t.Fatal(err)
	}
	createBound, err := mutationbind.CreateInput(createFrozen, registry)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 6, 10, 11, 12, 123_456_789, time.UTC)
	createBound, err = newMutationRuntimeValuesFrom(createdAt, bytes.NewReader(make([]byte, 16))).apply(createBound, registry)
	if err != nil {
		t.Fatal(err)
	}
	createProgram := render(mutationir.Create, &createBound, nil, nil)
	if got := createProgram.AuthoredFields(); len(got) != 1 || got[0] != policyir.FieldID(handle) {
		t.Fatalf("create authored fields = %x, want handle only", got)
	}
	created, err := executeScalarMutationProgram(ctx, database, provider, registry, policyir.ModelID(model), databaseExecution(database), createProgram)
	if err != nil {
		t.Fatal(err)
	}
	createdRow, ok := created.statement(0)
	if !ok {
		t.Fatal("create result row is absent")
	}
	createdValues := runtimePolicyValues(createdRow.cells)
	uuid, ok := createdValues[policyir.FieldID(id)].UUID()
	if !ok {
		t.Fatal("persisted generated UUID is absent")
	}
	createdSeconds, createdNanos, ok := createdValues[policyir.FieldID(updated)].DateTime()
	if !ok || createdSeconds != createdAt.Unix() || createdNanos != 123_456_000 {
		t.Fatalf("persisted create timestamp = (%d,%d,%v)", createdSeconds, createdNanos, ok)
	}

	modelFact, _ := registry.Model(model)
	identities := modelFact.Identities()
	if len(identities) == 0 {
		t.Fatal("user identity is absent")
	}
	selector := golem.GeneratedUniqueSelectorValue[runtimeValuesUser](model, identities[0].KeyID(), golem.GeneratedSelectorComponent(id, golem.UUID(uuid)))
	frozenTarget, err := golem.RuntimeFreezeMutationTarget[runtimeValuesUser](selector)
	if err != nil {
		t.Fatal(err)
	}
	boundTarget, err := mutationbind.Target(frozenTarget, model, registry)
	if err != nil {
		t.Fatal(err)
	}
	updatePublic := golem.GeneratedUpdateInput(model, golem.GeneratedSetFieldValue(model, handleField, "renamed"))
	updateFrozen, err := golem.RuntimeFreezeUpdateInput(updatePublic)
	if err != nil {
		t.Fatal(err)
	}
	updateBound, err := mutationbind.UpdateInput(updateFrozen, registry)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := createdAt.Add(2 * time.Second)
	updateBound, err = newMutationRuntimeValuesFrom(updatedAt, bytes.NewReader(nil)).apply(updateBound, registry)
	if err != nil {
		t.Fatal(err)
	}
	updateProgram := render(mutationir.Update, nil, &updateBound, &boundTarget)
	if got := updateProgram.AuthoredFields(); len(got) != 1 || got[0] != policyir.FieldID(handle) {
		t.Fatalf("update authored fields = %x, want handle only", got)
	}
	updatedResult, err := executeScalarMutationProgram(ctx, database, provider, registry, policyir.ModelID(model), databaseExecution(database), updateProgram)
	if err != nil {
		t.Fatal(err)
	}
	identity := updateProgram.IdentityVerification()
	afterStatement := identity.AfterStatement()
	updatedRow, ok := updatedResult.statement(afterStatement)
	if !ok {
		t.Fatal("update result row is absent")
	}
	updatedValues := runtimePolicyValues(updatedRow.cells)
	seconds, nanos, ok := updatedValues[policyir.FieldID(updated)].DateTime()
	if !ok || seconds != updatedAt.Unix() || nanos != 123_456_000 {
		t.Fatalf("persisted update timestamp = (%d,%d,%v)", seconds, nanos, ok)
	}
}

func runtimePolicyValues(cells []readdecode.Cell) map[policyir.FieldID]policyir.Value {
	result := make(map[policyir.FieldID]policyir.Value, len(cells))
	for _, cell := range cells {
		if value, ok := cell.PolicyValue(); ok {
			result[cell.FieldID()] = value
		}
	}
	return result
}

func runtimeOperationsByField(input mutationbind.ScalarInput) map[policyir.FieldID]mutationir.ScalarOperation {
	result := make(map[policyir.FieldID]mutationir.ScalarOperation, len(input.Operations()))
	for _, operation := range input.Operations() {
		result[operation.FieldID()] = operation
	}
	return result
}

func runtimeOperationValue(t *testing.T, operation mutationir.ScalarOperation) policyir.Value {
	t.Helper()
	value, ok := operation.Value()
	if !ok {
		t.Fatal("operation has no value")
	}
	return value
}

func runtimeValuesRegistry(t *testing.T) (*schema.Registry, golem.ModelID, golem.FieldID, golem.FieldID, golem.FieldID) {
	t.Helper()
	fixture := newRuntimeValuesSchemaFixture(t, "runtime_values")
	return fixture.registry, fixture.model, fixture.id, fixture.handle, fixture.updated
}

type runtimeValuesSchemaFixture struct {
	registry *schema.Registry
	postgres physical.PhysicalSchema
	model    golem.ModelID
	id       golem.FieldID
	handle   golem.FieldID
	updated  golem.FieldID
}

func newRuntimeValuesSchemaFixture(t *testing.T, namespace physical.PhysicalName) runtimeValuesSchemaFixture {
	t.Helper()
	compilation := gentest.SocialCompilationIR()
	user := &compilation.Model.Models[0]
	user.Fields[0].Scalar.Default = &compilerir.DefaultIR{Kind: compilerir.DefaultUUID, Producer: compilerir.ProducerApplication}
	user.Fields[2].Scalar.Updated = true
	modelDocument := runtimeValuesDocument(t, uint32(compilerir.ModelFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalModel(compilation.Model)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ModelFingerprint(compilation.Model)
		return payload, fingerprint, err
	})
	contractDocument := runtimeValuesDocument(t, uint32(compilerir.ContractFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalContract(compilation.Contract)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
		return payload, fingerprint, err
	})
	sqliteSchema, err := sqliteprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	postgresSchema, err := postgresprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	postgresSchema.System.Namespace.Name = physical.PhysicalName(string(namespace) + "_system")
	bundle := golem.GeneratedSchemaBundle(golem.SchemaDigest{0x74}, "runtime-values-test", "p4", modelDocument, contractDocument,
		runtimeValuesProviderDocument(t, golem.SQLite, sqliteSchema), runtimeValuesProviderDocument(t, golem.PostgreSQL, postgresSchema))
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeValuesSchemaFixture{
		registry: registry, postgres: postgresSchema,
		model: runtimeValuesFixedID[golem.ModelID](t, string(user.ID)), id: runtimeValuesFixedID[golem.FieldID](t, string(user.Fields[0].ID)),
		handle: runtimeValuesFixedID[golem.FieldID](t, string(user.Fields[1].ID)), updated: runtimeValuesFixedID[golem.FieldID](t, string(user.Fields[2].ID)),
	}
}

func runtimeValuesDocument(t *testing.T, version uint32, build func() ([]byte, compilerir.Fingerprint, error)) golem.SchemaDocument {
	t.Helper()
	payload, fingerprint, err := build()
	if err != nil {
		t.Fatal(err)
	}
	return golem.GeneratedSchemaDocument(version, uint32(compilerir.CanonicalFormatVersion), runtimeValuesDigest(t, string(fingerprint)), payload)
}

func runtimeValuesProviderDocument(t *testing.T, provider golem.Provider, value physical.PhysicalSchema) golem.ProviderSchemaDocument {
	t.Helper()
	payload, err := physical.CanonicalEncode(value)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := physical.PhysicalFingerprint(value)
	if err != nil {
		t.Fatal(err)
	}
	system, err := physical.SystemFingerprint(value.Provider, value.System)
	if err != nil {
		t.Fatal(err)
	}
	document := golem.GeneratedSchemaDocument(value.Version, value.CanonicalVersion, golem.SchemaDigest(fingerprint), payload)
	return golem.GeneratedProviderSchemaDocument(provider, golem.SchemaDigest(system), document)
}

func runtimeValuesDigest(t *testing.T, value string) golem.SchemaDigest {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("decode digest %q: %v", value, err)
	}
	var result golem.SchemaDigest
	copy(result[:], decoded)
	return result
}

func runtimeValuesFixedID[T ~[16]byte](t *testing.T, value string) T {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		t.Fatalf("decode ID %q: %v", value, err)
	}
	var result T
	copy(result[:], decoded)
	return result
}
