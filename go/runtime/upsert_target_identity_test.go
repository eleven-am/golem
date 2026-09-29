package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func runRuntimeDefaultGraphProfiles(t *testing.T, hooks func(schematest.GraphFixture) []golem.HookBinding[graphMutationActor], run func(*testing.T, graphMutationFixture)) {
	t.Helper()
	if hooks == nil {
		hooks = func(schematest.GraphFixture) []golem.HookBinding[graphMutationActor] { return nil }
	}
	t.Run("sqlite", func(t *testing.T) {
		schemaFixture := schematest.NewSubscribedGraphRuntimeDefaults(t)
		run(t, newGraphMutationFixtureWithHooks(t, schemaFixture, golem.ModelID{}, hooks(schemaFixture)))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			namespace := physical.PhysicalName(fmt.Sprintf("golem_upsert_identity_%s_%d_%d", profile.name, os.Getpid(), sequence))
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
			run(t, openGraphMutationFixtureWithHooks(t, database, golem.PostgreSQL, schemaFixture, golem.ModelID{}, hooks(schemaFixture)))
		})
	}
}

func graphRowCount[P, A any](t *testing.T, app *App[P, A], model golem.ModelID) int {
	t.Helper()
	var count int
	if err := app.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM `+nestedAcceptanceTable(app, model)); err != nil {
		t.Fatal(err)
	}
	return count
}

func graphUserTarget(fixture graphMutationFixture, id byte) golem.UniqueSelectorValue[graphMutationUser] {
	return golem.GeneratedUniqueSelectorValue[graphMutationUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: id}))
}

func graphUserCreate(fixture graphMutationFixture, id *byte, name string) golem.CreateInput[graphMutationUser] {
	values := []golem.CreateValue[graphMutationUser]{golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, name)}
	if id != nil {
		values = append(values, golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: *id}))
	}
	return golem.GeneratedCreateInput(fixture.schema.User, values...)
}

func graphUserRename(fixture graphMutationFixture, name string) golem.UpdateInput[graphMutationUser] {
	return golem.GeneratedUpdateInput(fixture.schema.User, golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, name))
}

func assertTargetIdentityRefusal(t *testing.T, err error) {
	t.Helper()
	assertBadUserInput(t, err)
	chain := ""
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		chain += cause.Error() + " <- "
	}
	if !strings.Contains(chain, "target selector") {
		t.Fatalf("upsert refusal chain=%q; want the target-selector refusal", chain)
	}
}

func assertNestedTargetIdentityRefusal(t *testing.T, err error) {
	t.Helper()
	assertTargetIdentityRefusal(t, err)
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Message != "upsert create input does not set the target selector" {
		t.Fatalf("nested upsert public error=%#v; want the target-selector message", failure)
	}
}

func assertBadUserInput(t *testing.T, err error) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
		t.Fatalf("upsert error=%v failure=%#v; want %s", err, failure, golem.CodeBadUserInput)
	}
}

func TestCreateMustSetEveryTargetSelectorComponent(t *testing.T) {
	uuidType, err := policyir.NewTypeRef(policyir.ValueUUID, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	region, id := policyir.FieldID{15: 1}, policyir.FieldID{15: 2}
	value := func(last byte) policyir.Value { return policyir.UUIDValue([16]byte{15: last}) }
	set := func(field policyir.FieldID, last byte) mutationir.ScalarOperation {
		operation, err := mutationir.NewSet(field, uuidType, value(last))
		if err != nil {
			t.Fatal(err)
		}
		return operation
	}
	regionSelector, err := mutationir.NewSelectorValue(region, value(7))
	if err != nil {
		t.Fatal(err)
	}
	idSelector, err := mutationir.NewSelectorValue(id, value(8))
	if err != nil {
		t.Fatal(err)
	}
	target, err := mutationir.NewTarget(policyir.ModelID{15: 9}, golem.KeyID{15: 10}, []mutationir.SelectorValue{regionSelector, idSelector}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCreateAgreesWithTarget(target, []mutationir.ScalarOperation{set(region, 7), set(id, 8)}); err != nil {
		t.Fatalf("agreeing create was refused: %v", err)
	}
	for name, operations := range map[string][]mutationir.ScalarOperation{
		"omitted component":      {set(id, 8)},
		"contradicted component": {set(region, 7), set(id, 9)},
		"no components":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			assertTargetIdentityRefusal(t, validateCreateAgreesWithTarget(target, operations))
		})
	}
}

func TestRootUpsertRefusesCreateThatWouldNotCarryTheTargetIdentity(t *testing.T) {
	runRuntimeDefaultGraphProfiles(t, nil, func(t *testing.T, fixture graphMutationFixture) {
		ctx := context.Background()
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		before := graphRowCount(t, fixture.app, fixture.schema.User)
		for attempt := 0; attempt < 2; attempt++ {
			_, err := SystemUpsert(ctx, fixture.app.System(), fixture.userDescriptor, graphUserTarget(fixture, 50), graphUserCreate(fixture, nil, "omitted"), graphUserRename(fixture, "unused"))
			assertTargetIdentityRefusal(t, err)
			_, err = CallerUpsert(ctx, caller, fixture.userDescriptor, graphUserTarget(fixture, 50), graphUserCreate(fixture, nil, "omitted"), graphUserRename(fixture, "unused"))
			assertTargetIdentityRefusal(t, err)
		}
		if after := graphRowCount(t, fixture.app, fixture.schema.User); after != before {
			t.Fatalf("refused upserts created %d rows", after-before)
		}
		explicit := byte(51)
		for attempt := 0; attempt < 2; attempt++ {
			if _, err := SystemUpsert(ctx, fixture.app.System(), fixture.userDescriptor, graphUserTarget(fixture, 51), graphUserCreate(fixture, &explicit, "explicit"), graphUserRename(fixture, "renamed")); err != nil {
				t.Fatal(err)
			}
			if got := graphRowCount(t, fixture.app, fixture.schema.User); got != before+1 {
				t.Fatalf("explicit upsert rows=%d want=%d", got, before+1)
			}
		}
		if _, err := SystemUpsert(ctx, fixture.app.System(), fixture.userDescriptor, graphUserTarget(fixture, 51), graphUserCreate(fixture, nil, "never-created"), graphUserRename(fixture, "renamed-again")); err != nil {
			t.Fatalf("update branch refused an unexecuted create without the identity: %v", err)
		}
	})
}

func TestGraphQLUpsertRefusesCreateThatWouldNotCarryTheTargetIdentity(t *testing.T) {
	runRuntimeDefaultGraphProfiles(t, nil, func(t *testing.T, fixture graphMutationFixture) {
		ctx := context.Background()
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		execution, err := NewCallerMutationExecution(caller, CallerMutationModel[graphMutationPrincipal, graphMutationActor](fixture.userDescriptor))
		if err != nil {
			t.Fatal(err)
		}
		target, err := golem.RuntimeFreezeMutationTarget[graphMutationUser](graphUserTarget(fixture, 53))
		if err != nil {
			t.Fatal(err)
		}
		create, err := golem.RuntimeFreezeCreateInput(graphUserCreate(fixture, nil, "graphql"))
		if err != nil {
			t.Fatal(err)
		}
		update, err := golem.RuntimeFreezeUpdateInput(graphUserRename(fixture, "unused"))
		if err != nil {
			t.Fatal(err)
		}
		projection, err := golem.FreezeFindMany(fixture.userDescriptor, golem.Select[graphMutationUser](fixture.userName))
		if err != nil {
			t.Fatal(err)
		}
		request, err := golem.RuntimeFreezeMutationRequest(golem.RuntimeMutationRequestInput{Operation: golem.RuntimeMutationUpsert, Model: fixture.schema.User, Target: &target, Create: &create, Update: &update, Projection: &projection})
		if err != nil {
			t.Fatal(err)
		}
		before := graphRowCount(t, fixture.app, fixture.schema.User)
		for attempt := 0; attempt < 2; attempt++ {
			_, err := execution.ExecuteFrozenMutation(ctx, request)
			assertTargetIdentityRefusal(t, err)
		}
		if after := graphRowCount(t, fixture.app, fixture.schema.User); after != before {
			t.Fatalf("refused GraphQL upserts created %d rows", after-before)
		}
	})
}

func TestHookExecutorUpsertRefusesCreateThatWouldNotCarryTheTargetIdentity(t *testing.T) {
	var hookErr error
	hooks := func(schema schematest.GraphFixture) []golem.HookBinding[graphMutationActor] {
		descriptor := golem.GeneratedModelDescriptor[graphMutationUser](schema.User, golem.GeneratedDescriptorShape(
			[]golem.FieldID{schema.UserID, schema.UserName}, nil,
			[]golem.IdentityMetadata{golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)}, nil,
		))
		name := golem.GeneratedTextField[graphMutationUser, string](schema.UserName)
		return []golem.HookBinding[graphMutationActor]{
			golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(ctx context.Context, result golem.CreateHookResult[graphMutationPost]) error {
				target := golem.GeneratedUniqueSelectorValue[graphMutationUser](schema.User, schema.UserKey, golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 52}))
				create := golem.GeneratedCreateInput(schema.User, golem.GeneratedCreateFieldValue(schema.User, name, "from-hook"))
				update := golem.GeneratedUpdateInput(schema.User, golem.GeneratedSetFieldValue(schema.User, name, "unused"))
				_, hookErr = golem.HookUpsertRow(ctx, result.Executor(), descriptor, target, create, update)
				return hookErr
			}),
		}
	}
	runRuntimeDefaultGraphProfiles(t, hooks, func(t *testing.T, fixture graphMutationFixture) {
		ctx := context.Background()
		hookErr = nil
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		owner := byte(55)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, graphUserCreate(fixture, &owner, "owner")); err != nil {
			t.Fatal(err)
		}
		users := graphRowCount(t, fixture.app, fixture.schema.User)
		authorID := golem.GeneratedEqualField[graphMutationPost, golem.UUID](fixture.schema.AuthorID)
		post := golem.GeneratedCreateInput(fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 56}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, authorID, golem.UUID{15: owner}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "hooked"),
		)
		if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, post); err == nil {
			t.Fatal("create succeeded although its hook upsert was refused")
		}
		assertTargetIdentityRefusal(t, hookErr)
		if got := graphRowCount(t, fixture.app, fixture.schema.User); got != users {
			t.Fatalf("refused hook upsert created %d users", got-users)
		}
		if got := graphRowCount(t, fixture.app, fixture.schema.Post); got != 0 {
			t.Fatalf("rolled-back create left %d posts", got)
		}
	})
}

func TestNestedUpsertRefusesCreateThatWouldNotCarryTheTargetIdentity(t *testing.T) {
	runRuntimeDefaultGraphProfiles(t, nil, func(t *testing.T, fixture graphMutationFixture) {
		ctx := context.Background()
		owner := byte(60)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, graphUserCreate(fixture, &owner, "owner")); err != nil {
			t.Fatal(err)
		}
		postTarget := golem.GeneratedUniqueSelectorValue[graphMutationPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 61}))
		postCreate := func(id *byte) golem.CreateInput[graphMutationPost] {
			values := []golem.CreateValue[graphMutationPost]{golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "nested")}
			if id != nil {
				values = append(values, golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: *id}))
			}
			return golem.GeneratedCreateInput(fixture.schema.Post, values...)
		}
		postUpdate := golem.GeneratedUpdateInput(fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postTitle, "nested-updated"))
		nested := func(id *byte) golem.UpdateInput[graphMutationUser] {
			return golem.GeneratedUpdateInput(fixture.schema.User,
				golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "owner-touched"),
				golem.GeneratedNestedUpsert[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget, postCreate(id), postUpdate),
			)
		}
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			_, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, graphUserTarget(fixture, owner), nested(nil))
			assertNestedTargetIdentityRefusal(t, err)
			_, err = CallerUpdate(ctx, caller, fixture.userDescriptor, graphUserTarget(fixture, owner), nested(nil))
			assertNestedTargetIdentityRefusal(t, err)
		}
		if got := graphRowCount(t, fixture.app, fixture.schema.Post); got != 0 {
			t.Fatalf("refused nested upserts created %d posts", got)
		}
		explicit := byte(61)
		if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, graphUserTarget(fixture, owner), nested(&explicit)); err != nil {
			t.Fatal(err)
		}
		if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, graphUserTarget(fixture, owner), nested(nil)); err != nil {
			t.Fatalf("nested update branch refused an unexecuted create without the identity: %v", err)
		}
		if got := graphRowCount(t, fixture.app, fixture.schema.Post); got != 1 {
			t.Fatalf("nested upserts left %d posts, want 1", got)
		}
	})
}

func TestHookedNestedUpsertRefusesCreateThatWouldNotCarryTheTargetIdentity(t *testing.T) {
	var beforeCreate int
	hooks := func(schema schematest.GraphFixture) []golem.HookBinding[graphMutationActor] {
		return []golem.HookBinding[graphMutationActor]{
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationPost]) error {
				beforeCreate++
				return nil
			}),
		}
	}
	runRuntimeDefaultGraphProfiles(t, hooks, func(t *testing.T, fixture graphMutationFixture) {
		ctx := context.Background()
		beforeCreate = 0
		owner := byte(62)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, graphUserCreate(fixture, &owner, "owner")); err != nil {
			t.Fatal(err)
		}
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		postTarget := golem.GeneratedUniqueSelectorValue[graphMutationPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 63}))
		postCreate := golem.GeneratedCreateInput(fixture.schema.Post, golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "hooked-nested"))
		postUpdate := golem.GeneratedUpdateInput(fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postTitle, "unused"))
		nested := golem.GeneratedUpdateInput(fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "owner-touched"),
			golem.GeneratedNestedUpsert[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget, postCreate, postUpdate),
		)
		_, err = CallerUpdate(ctx, caller, fixture.userDescriptor, graphUserTarget(fixture, owner), nested)
		assertNestedTargetIdentityRefusal(t, err)
		if beforeCreate == 0 {
			t.Fatal("the nested create hook did not run, so the hooked path was not exercised")
		}
		if got := graphRowCount(t, fixture.app, fixture.schema.Post); got != 0 {
			t.Fatalf("refused hooked nested upsert created %d posts", got)
		}
	})
}

func TestCompositeUpsertRefusesCreateMissingOneTargetComponent(t *testing.T) {
	run := func(t *testing.T, fixture compositeMutationFixture) {
		ctx := context.Background()
		uuid := func(value byte) golem.UUID { return golem.UUID{15: value} }
		tenantTarget := golem.GeneratedUniqueSelectorValue[compositeMutationTenant](fixture.schema.Tenant, fixture.schema.TenantKey,
			golem.GeneratedSelectorComponent(fixture.schema.TenantRegion, uuid(71)), golem.GeneratedSelectorComponent(fixture.schema.TenantID, uuid(72)))
		tenantCreate := func(values ...golem.CreateValue[compositeMutationTenant]) golem.CreateInput[compositeMutationTenant] {
			return golem.GeneratedCreateInput(fixture.schema.Tenant, values...)
		}
		unusedTenantUpdate := golem.GeneratedUpdateInput(fixture.schema.Tenant, golem.GeneratedSetFieldValue(fixture.schema.Tenant, fixture.tenantRegion, uuid(71)))
		for _, create := range []golem.CreateInput[compositeMutationTenant]{
			tenantCreate(golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantID, uuid(72))),
			tenantCreate(golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantRegion, uuid(71)), golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantID, uuid(79))),
		} {
			_, err := SystemUpsert(ctx, fixture.app.System(), fixture.tenantDescriptor, tenantTarget, create, unusedTenantUpdate)
			assertBadUserInput(t, err)
		}
		if got := graphRowCount(t, fixture.app, fixture.schema.Tenant); got != 0 {
			t.Fatalf("refused composite upserts created %d tenants", got)
		}
		owner := tenantCreate(golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantRegion, uuid(71)), golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantID, uuid(72)))
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.tenantDescriptor, owner); err != nil {
			t.Fatal(err)
		}
		itemTarget := golem.GeneratedUniqueSelectorValue[compositeMutationItem](fixture.schema.Item, fixture.schema.ItemKey,
			golem.GeneratedSelectorComponent(fixture.schema.ItemRegion, uuid(81)), golem.GeneratedSelectorComponent(fixture.schema.ItemID, uuid(82)))
		itemCreate := golem.GeneratedCreateInput(fixture.schema.Item, golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.itemID, uuid(82)))
		itemUpdate := golem.GeneratedUpdateInput(fixture.schema.Item, golem.GeneratedSetFieldValue(fixture.schema.Item, fixture.itemID, uuid(82)))
		nested := golem.GeneratedUpdateInput(fixture.schema.Tenant,
			golem.GeneratedNestedUpsert[compositeMutationTenant, compositeMutationItem](fixture.schema.Tenant, fixture.schema.TenantItems, fixture.schema.Ownership, fixture.schema.Item, itemTarget, itemCreate, itemUpdate),
		)
		_, err := SystemUpdate(ctx, fixture.app.System(), fixture.tenantDescriptor, tenantTarget, nested)
		assertBadUserInput(t, err)
		if got := graphRowCount(t, fixture.app, fixture.schema.Item); got != 0 {
			t.Fatalf("refused composite nested upsert created %d items", got)
		}
	}
	t.Run("sqlite", func(t *testing.T) { run(t, newCompositeMutationFixture(t)) })
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			run(t, newPostgresCompositeMutationFixture(t, profile))
		})
	}
}
