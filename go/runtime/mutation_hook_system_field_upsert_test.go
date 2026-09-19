package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func withPostContract(t testing.TB, fixture schematest.Fixture, patch func(*compilerir.ModelContractIR)) schematest.Fixture {
	t.Helper()
	contractDocument := fixture.Bundle.Contract()
	var contract compilerir.ContractIR
	if err := json.Unmarshal(contractDocument.Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	found := false
	for index := range contract.Models {
		if contract.Models[index].ModelID == compilerir.ModelID(fmt.Sprintf("%x", fixture.Post[:])) {
			patch(&contract.Models[index])
			found = true
		}
	}
	if !found {
		t.Fatal("Post contract is absent")
	}
	payload, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(raw) != len(golem.SchemaDigest{}) {
		t.Fatalf("contract fingerprint=%q error=%v", fingerprint, err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], raw)
	contractDocument = golem.GeneratedSchemaDocument(contractDocument.FormatVersion(), contractDocument.CanonicalVersion(), digest, payload)
	fixture.Bundle = golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		fixture.Bundle.Model(), contractDocument, fixture.Bundle.Providers()...,
	)
	fixture.Registry, err = policyschema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func withSystemAuthor(t testing.TB, fixture schematest.Fixture, hookOwnedCreate bool, modes ...compilerir.FieldMode) schematest.Fixture {
	t.Helper()
	author := compilerir.FieldID(fmt.Sprintf("%x", fixture.AuthorID[:]))
	return withPostContract(t, fixture, func(model *compilerir.ModelContractIR) {
		for index := range model.Fields {
			if model.Fields[index].FieldID == author {
				model.Fields[index].Modes = append([]compilerir.FieldMode(nil), modes...)
			}
		}
		if hookOwnedCreate {
			model.HookOwnedCreateFields = []compilerir.FieldID{author}
		}
	})
}

func systemAuthorUpsertFixture(t *testing.T, hookOwnedCreate bool, hooks func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor], modes ...compilerir.FieldMode) mutationResultFixture {
	t.Helper()
	if len(modes) == 0 {
		modes = []compilerir.FieldMode{compilerir.ModeSystem}
	}
	schemaFixture := withSystemAuthor(t, schematest.New(t), hookOwnedCreate, modes...)
	fixture := openMutationResultFixture(t, schemaFixture, MutationLimits{}, hooks, nil, nil, true)
	if _, err := fixture.app.database.ExecContext(context.Background(), `INSERT INTO "users"("id","name") VALUES (?,?)`, mutationResultUUIDText(3), "carol"); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func updateHookSettingAuthor(author golem.UUID) func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
	return func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		field := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
		return []golem.HookBinding[mutationResultActor]{
			createHookSettingAuthor(author)(schema, title)[0],
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, title, "hook kept this"),
					golem.GeneratedSetFieldValue(schema.Post, field, author),
				))
				return nil
			}),
		}
	}
}

func createHookSettingAuthor(author golem.UUID) func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
	return func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		capability := golem.GeneratedCreateFieldCapability(schema.Post, golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID))
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
				return golem.SetCreate(request, capability, author)
			}),
		}
	}
}

func postWithoutAuthor(fixture mutationResultFixture, id byte, title string) golem.CreateInput[mutationResultPost] {
	return golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, title),
	)
}

func TestUpsertUpdateBranchHookCannotWriteASystemImmutableField(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	fixture := systemAuthorUpsertFixture(t, true, updateHookSettingAuthor(bob), compilerir.ModeSystem, compilerir.ModeImmutable)
	seedSystemAuthorPost(t, ctx, fixture, 85, alice)
	caller := mustMutationResultCaller(t, fixture)
	_, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(85), postWithoutAuthor(fixture, 85, "unused"), fixture.updateTitle("caller wrote this"))
	requireRefusal(t, err, "immutable field is writable only during create", "an upsert update hook rewrote a system;immutable field")
	if got := readSystemAuthor(t, ctx, fixture, 85); got != alice {
		t.Fatalf("persisted author=%v, want the seeded value", got)
	}
}

func systemAuthorPost(fixture mutationVocabularyFixture, id byte, author *golem.UUID, title string) golem.CreateInput[mutationResultPost] {
	decimal, _ := golem.ParseDecimal("1.25")
	values := []golem.CreateValue[mutationResultPost]{
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, title),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, golem.GeneratedEqualField[mutationResultPost, golem.Decimal](fixture.schema.PostDecimal), decimal),
	}
	if author != nil {
		values = append(values, golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, *author))
	}
	if model, present := fixture.app.registry.Model(fixture.schema.Post); present {
		if _, versioned := model.OptimisticConcurrency(); !versioned {
			values = append(values, golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.bigInt, int64(1)))
		}
	}
	return golem.GeneratedCreateInput(fixture.schema.Post, values...)
}

func seedSystemAuthorVocabularyPost(t *testing.T, fixture mutationVocabularyFixture, id byte, author golem.UUID) {
	t.Helper()
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, systemAuthorPost(fixture, id, &author, "seeded")); err != nil {
		t.Fatal(err)
	}
}

func systemAuthorHooks(inputs func(mutationVocabularyFixture) golem.UpdateInput[mutationResultPost]) func(mutationVocabularyFixture) []golem.HookBinding[mutationResultActor] {
	return func(fixture mutationVocabularyFixture) []golem.HookBinding[mutationResultActor] {
		capability := golem.GeneratedCreateFieldCapability(fixture.schema.Post, fixture.authorID)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](fixture.schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(inputs(fixture))
				return nil
			}),
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
				return golem.SetCreate(request, capability, golem.UUID{15: 3})
			}),
		}
	}
}

func hookTitleAndAuthor(author golem.UUID) func(mutationVocabularyFixture) golem.UpdateInput[mutationResultPost] {
	return func(fixture mutationVocabularyFixture) golem.UpdateInput[mutationResultPost] {
		return golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "hook kept this"),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, author),
		)
	}
}

func requireRefusal(t *testing.T, err error, reason string, message string) {
	t.Helper()
	if err == nil {
		t.Fatal(message)
	}
	if chain := errorChain(err); !strings.Contains(chain, reason) {
		t.Fatalf("%s: refused for the wrong reason, want %q in %s", message, reason, chain)
	}
}

func TestVersionedUpsertExistingHookWritesASystemField(t *testing.T) {
	forEachSystemAuthorProvider(t, true, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		ctx := context.Background()
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 2})))
		seedSystemAuthorVocabularyPost(t, fixture, 92, golem.UUID{15: 1})
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(92), golem.ExpectExisting(1), systemAuthorPost(fixture, 92, nil, "unused"), fixture.updateTitle("caller wrote this")); err != nil {
			t.Fatalf("hook-authored system write on a versioned upsert update was refused: %v: %v", err, errorChain(err))
		}
		if author, version := readPostAuthor(t, fixture, 92); author != mutationResultUUIDText(2) || version != 2 {
			t.Fatalf("persisted author=%s version=%d, want the hook's author at version 2", author, version)
		}
	})
}

func TestVersionedUpsertAbsentCreateHookWritesASystemField(t *testing.T) {
	ctx := context.Background()
	fixture := versionedSystemAuthorFixture(t, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 2})))
	caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
	if _, err := CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(93), golem.ExpectAbsent(), systemAuthorPost(fixture, 93, nil, "created"), fixture.updateTitle("unused")); err != nil {
		t.Fatalf("hook-authored system write on a versioned upsert create was refused: %v: %v", err, errorChain(err))
	}
	if author, version := readPostAuthor(t, fixture, 93); author != mutationResultUUIDText(3) || version != 1 {
		t.Fatalf("persisted author=%s version=%d, want the create hook's author at version 1", author, version)
	}
}

func TestVersionedUpsertAbsentCallerSystemFieldStaysRefusedWhenAHookRewritesIt(t *testing.T) {
	ctx := context.Background()
	fixture := versionedSystemAuthorFixture(t, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 3})))
	caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
	bob := golem.UUID{15: 2}
	_, err := CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(98), golem.ExpectAbsent(), systemAuthorPost(fixture, 98, &bob, "forged"), fixture.updateTitle("unused"))
	requireRefusal(t, err, "system field is not caller writable", "a hook rewriting the caller's own system-field value laundered it into a versioned upsert create")
	if count := countPosts(t, fixture.mutationResultFixture, 98); count != 0 {
		t.Fatalf("refused versioned upsert create persisted %d rows", count)
	}
}

func TestVersionedUpdateHookCannotWriteASystemImmutableField(t *testing.T) {
	ctx := context.Background()
	fixture := versionedSystemAuthorFixture(t, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 2})), compilerir.ModeSystem, compilerir.ModeImmutable)
	seedSystemAuthorVocabularyPost(t, fixture, 95, golem.UUID{15: 1})
	caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
	_, err := CallerUpdateVersioned(ctx, caller, fixture.postDescriptor, fixture.target(95), golem.ExpectVersion(1), fixture.updateTitle("caller wrote this"))
	requireRefusal(t, err, "immutable field is writable only during create", "a versioned update hook rewrote a system;immutable field")
	_, err = CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(95), golem.ExpectExisting(1), systemAuthorPost(fixture, 95, nil, "unused"), fixture.updateTitle("caller wrote this"))
	requireRefusal(t, err, "immutable field is writable only during create", "a versioned upsert update hook rewrote a system;immutable field")
	if author, version := readPostAuthor(t, fixture, 95); author != mutationResultUUIDText(1) || version != 1 {
		t.Fatalf("persisted author=%s version=%d, want the seeded row unchanged", author, version)
	}
}

func TestVersionedUpdateHookCannotWriteTheVersionToken(t *testing.T) {
	ctx := context.Background()
	fixture := versionedSystemAuthorFixture(t, true, systemAuthorHooks(func(fixture mutationVocabularyFixture) golem.UpdateInput[mutationResultPost] {
		return golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "hook kept this"),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.bigInt, int64(99)),
		)
	}))
	seedSystemAuthorVocabularyPost(t, fixture, 96, golem.UUID{15: 1})
	caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
	_, err := CallerUpdateVersioned(ctx, caller, fixture.postDescriptor, fixture.target(96), golem.ExpectVersion(1), fixture.updateTitle("caller wrote this"))
	requireRefusal(t, err, "optimistic-concurrency field cannot be application authored", "a versioned update hook wrote the version token")
	_, err = CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(96), golem.ExpectExisting(1), systemAuthorPost(fixture, 96, nil, "unused"), fixture.updateTitle("caller wrote this"))
	requireRefusal(t, err, "optimistic-concurrency field cannot be application authored", "a versioned upsert update hook wrote the version token")
	var title string
	if err := fixture.app.database.GetContext(ctx, &title, `SELECT "title" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(96)); err != nil || title != "seeded" {
		t.Fatalf("persisted title=%q err=%v, want the seeded row unchanged", title, err)
	}
	if _, version := readPostAuthor(t, fixture, 96); version != 1 {
		t.Fatalf("persisted version=%d, want 1", version)
	}
}

func TestVersionedUpsertCreateHookCannotWriteTheVersionToken(t *testing.T) {
	ctx := context.Background()
	fixture := versionedSystemAuthorFixture(t, true, func(fixture mutationVocabularyFixture) []golem.HookBinding[mutationResultActor] {
		author := golem.GeneratedCreateFieldCapability(fixture.schema.Post, fixture.authorID)
		version := golem.GeneratedCreateFieldCapability(fixture.schema.Post, fixture.bigInt)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
				if err := golem.SetCreate(request, author, golem.UUID{15: 2}); err != nil {
					return err
				}
				return golem.SetCreate(request, version, int64(99))
			}),
		}
	})
	caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
	_, err := CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(97), golem.ExpectAbsent(), systemAuthorPost(fixture, 97, nil, "created"), fixture.updateTitle("unused"))
	requireRefusal(t, err, "optimistic-concurrency field cannot be application authored", "a versioned upsert create hook wrote the version token")
	if count := countPosts(t, fixture.mutationResultFixture, 97); count != 0 {
		t.Fatalf("refused upsert create persisted %d rows", count)
	}
}

type hookExecutorInvocation func(context.Context, golem.HookExecutor, mutationResultFixture) error

func hookExecutorSystemAuthorFixture(t *testing.T, hookAuthor golem.UUID, invoke hookExecutorInvocation, modes ...compilerir.FieldMode) (mutationResultFixture, *error) {
	t.Helper()
	var fixture mutationResultFixture
	inner := new(error)
	fixture = systemAuthorUpsertFixture(t, true, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		bindings := updateHookSettingAuthor(hookAuthor)(schema, title)
		return append(bindings, golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookResult[mutationResultUser]](schema.User, golem.HookUpdate, func(hookContext context.Context, result golem.UpdateHookResult[mutationResultUser]) error {
			*inner = invoke(hookContext, result.Executor(), fixture)
			return *inner
		}))
	}, modes...)
	return fixture, inner
}

func triggerHookExecutor(ctx context.Context, t *testing.T, fixture mutationResultFixture) error {
	t.Helper()
	caller := mustMutationResultCaller(t, fixture)
	target := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
	_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, target, golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "trigger")))
	return err
}

func TestHookExecutorUpsertCreateBranchHookWritesASystemField(t *testing.T) {
	ctx := context.Background()
	bob := golem.UUID{15: 2}
	fixture, _ := hookExecutorSystemAuthorFixture(t, bob, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationResultFixture) error {
		_, err := golem.HookUpsertRow(hookContext, executor, fixture.postDescriptor, fixture.target(111), postWithoutAuthor(fixture, 111, "created"), fixture.updateTitle("unused"))
		return err
	})
	if err := triggerHookExecutor(ctx, t, fixture); err != nil {
		t.Fatalf("hook-authored system write on an executor upsert create was refused: %v: %v", err, errorChain(err))
	}
	if got := readSystemAuthor(t, ctx, fixture, 111); got != bob {
		t.Fatalf("persisted author=%v, want the value the create hook authored", got)
	}
}

func TestHookExecutorUpsertUpdateBranchHookWritesASystemField(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	fixture, _ := hookExecutorSystemAuthorFixture(t, bob, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationResultFixture) error {
		_, err := golem.HookUpsertRow(hookContext, executor, fixture.postDescriptor, fixture.target(112), postWithoutAuthor(fixture, 112, "unused"), fixture.updateTitle("caller wrote this"))
		return err
	})
	seedSystemAuthorPost(t, ctx, fixture, 112, alice)
	if err := triggerHookExecutor(ctx, t, fixture); err != nil {
		t.Fatalf("hook-authored system write on an executor upsert update was refused: %v: %v", err, errorChain(err))
	}
	if got := readSystemAuthor(t, ctx, fixture, 112); got != bob {
		t.Fatalf("persisted author=%v, want the value the update hook authored", got)
	}
}

func TestHookExecutorUpsertUpdateBranchCallerSystemFieldStaysRefusedWhenAHookRewritesIt(t *testing.T) {
	ctx := context.Background()
	alice, bob, carol := golem.UUID{15: 1}, golem.UUID{15: 2}, golem.UUID{15: 3}
	fixture, inner := hookExecutorSystemAuthorFixture(t, carol, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationResultFixture) error {
		forged := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, bob),
		)
		_, err := golem.HookUpsertRow(hookContext, executor, fixture.postDescriptor, fixture.target(113), postWithoutAuthor(fixture, 113, "unused"), forged)
		return err
	})
	seedSystemAuthorPost(t, ctx, fixture, 113, alice)
	if err := triggerHookExecutor(ctx, t, fixture); err == nil {
		t.Fatal("a hook rewriting the executor caller's own system-field value laundered it into an upsert update")
	}
	requireRefusal(t, *inner, "system field is not caller writable", "executor upsert update refusal")
	if got := readSystemAuthor(t, ctx, fixture, 113); got != alice {
		t.Fatalf("persisted author=%v, want the seeded value", got)
	}
}

func TestHookExecutorUpsertCreateBranchCallerSystemFieldStaysRefusedWhenAHookRewritesIt(t *testing.T) {
	ctx := context.Background()
	bob, carol := golem.UUID{15: 2}, golem.UUID{15: 3}
	fixture, inner := hookExecutorSystemAuthorFixture(t, carol, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationResultFixture) error {
		_, err := golem.HookUpsertRow(hookContext, executor, fixture.postDescriptor, fixture.target(114), fixture.createPost(114, bob, "forged"), fixture.updateTitle("unused"))
		return err
	})
	if err := triggerHookExecutor(ctx, t, fixture); err == nil {
		t.Fatal("a hook rewriting the executor caller's own system-field value laundered it into an upsert create")
	}
	requireRefusal(t, *inner, "system field is not caller writable", "executor upsert create refusal")
	if count := countPosts(t, fixture, 114); count != 0 {
		t.Fatalf("refused executor upsert create persisted %d rows", count)
	}
}

func TestHookExecutorUpsertUpdateBranchHookCannotWriteASystemImmutableField(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	fixture, inner := hookExecutorSystemAuthorFixture(t, bob, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationResultFixture) error {
		_, err := golem.HookUpsertRow(hookContext, executor, fixture.postDescriptor, fixture.target(115), postWithoutAuthor(fixture, 115, "unused"), fixture.updateTitle("caller wrote this"))
		return err
	}, compilerir.ModeSystem, compilerir.ModeImmutable)
	seedSystemAuthorPost(t, ctx, fixture, 115, alice)
	if err := triggerHookExecutor(ctx, t, fixture); err == nil {
		t.Fatal("an executor upsert update hook rewrote a system;immutable field")
	}
	requireRefusal(t, *inner, "immutable field is writable only during create", "executor upsert immutable refusal")
	if got := readSystemAuthor(t, ctx, fixture, 115); got != alice {
		t.Fatalf("persisted author=%v, want the seeded value", got)
	}
}

func TestHookExecutorUpdateHookWritesASystemField(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	fixture, _ := hookExecutorSystemAuthorFixture(t, bob, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationResultFixture) error {
		_, err := golem.HookUpdateRow(hookContext, executor, fixture.postDescriptor, fixture.target(116), fixture.updateTitle("caller wrote this"))
		return err
	})
	seedSystemAuthorPost(t, ctx, fixture, 116, alice)
	if err := triggerHookExecutor(ctx, t, fixture); err != nil {
		t.Fatalf("hook-authored system write on an executor update was refused: %v: %v", err, errorChain(err))
	}
	if got := readSystemAuthor(t, ctx, fixture, 116); got != bob {
		t.Fatalf("persisted author=%v, want the value the update hook authored", got)
	}
}

func TestHookExecutorUpdateCallerSystemFieldStaysRefusedWhenAHookRewritesIt(t *testing.T) {
	ctx := context.Background()
	alice, bob, carol := golem.UUID{15: 1}, golem.UUID{15: 2}, golem.UUID{15: 3}
	fixture, inner := hookExecutorSystemAuthorFixture(t, carol, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationResultFixture) error {
		forged := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, bob),
		)
		_, err := golem.HookUpdateRow(hookContext, executor, fixture.postDescriptor, fixture.target(117), forged)
		return err
	})
	seedSystemAuthorPost(t, ctx, fixture, 117, alice)
	if err := triggerHookExecutor(ctx, t, fixture); err == nil {
		t.Fatal("a hook rewriting the executor caller's own system-field value laundered it into an update")
	}
	requireRefusal(t, *inner, "system field is not caller writable", "executor update refusal")
	if got := readSystemAuthor(t, ctx, fixture, 117); got != alice {
		t.Fatalf("persisted author=%v, want the seeded value", got)
	}
}

var systemAuthorNamespaceSequence atomic.Uint64

func forEachSystemAuthorProvider(t *testing.T, versioned bool, run func(*testing.T, *sqlx.DB, golem.Provider, schematest.Fixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		schema := schematest.NewMutationVocabulary(t)
		if versioned {
			schema = schematest.NewOptimisticConcurrency(t)
		}
		run(t, openSystemAuthorSQLite(t, schema), golem.SQLite, schema)
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			sequence := systemAuthorNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_system_author_%s_%d_%d", profile.name, os.Getpid(), sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_system_author_sys_%s_%d_%d", profile.name, os.Getpid(), sequence))
			schema := schematest.NewMutationVocabularyPostgreSQLNamespaces(t, applicationNamespace, systemNamespace)
			if versioned {
				schema = schematest.NewOptimisticConcurrencyPostgreSQLNamespaces(t, applicationNamespace, systemNamespace)
			}
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(applicationNamespace)+`" CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, schema.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			run(t, database, golem.PostgreSQL, schema)
		})
	}
}

func openSystemAuthorSQLite(t *testing.T, schema schematest.Fixture) *sqlx.DB {
	t.Helper()
	ctx := context.Background()
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "system-author.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
		t.Fatal(err)
	}
	return database
}

func versionedSystemAuthorFixture(t *testing.T, hookOwnedCreate bool, hooks func(mutationVocabularyFixture) []golem.HookBinding[mutationResultActor], modes ...compilerir.FieldMode) mutationVocabularyFixture {
	t.Helper()
	schema := schematest.NewOptimisticConcurrency(t)
	return openSystemAuthorVocabularyFixture(t, openSystemAuthorSQLite(t, schema), golem.SQLite, schema, hookOwnedCreate, hooks, modes...)
}

func openSystemAuthorVocabularyFixture(t *testing.T, database *sqlx.DB, provider golem.Provider, base schematest.Fixture, hookOwnedCreate bool, hooks func(mutationVocabularyFixture) []golem.HookBinding[mutationResultActor], modes ...compilerir.FieldMode) mutationVocabularyFixture {
	t.Helper()
	ctx := context.Background()
	if len(modes) == 0 {
		modes = []compilerir.FieldMode{compilerir.ModeSystem}
	}
	schema := withSystemAuthor(t, base, hookOwnedCreate, modes...)
	fixture := openMutationVocabularyFixture(t, database, provider, schema)
	for index, name := range []string{"alice", "bob", "carol"} {
		user := golem.GeneratedCreateInput[mutationResultUser](schema.User,
			golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: byte(index + 1)}),
			golem.GeneratedCreateFieldValue(schema.User, fixture.userName, name),
		)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, user); err != nil {
			t.Fatal(err)
		}
	}
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		return rules.Freeze(schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		return rules.Freeze(schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, hooks(fixture)))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app.bindings = bindings
	return fixture
}

func countPosts(t *testing.T, fixture mutationResultFixture, id byte) int {
	t.Helper()
	var count int
	query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id" = ?`)
	if err := fixture.app.database.GetContext(context.Background(), &count, query, mutationResultUUIDText(id)); err != nil {
		t.Fatal(err)
	}
	return count
}

func readPostAuthor(t *testing.T, fixture mutationVocabularyFixture, id byte) (string, int64) {
	t.Helper()
	var author string
	var version int64
	query := fixture.app.database.Rebind(`SELECT "author_id", "big_int" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id" = ?`)
	if err := fixture.app.database.QueryRowxContext(context.Background(), query, mutationResultUUIDText(id)).Scan(&author, &version); err != nil {
		t.Fatal(err)
	}
	return author, version
}

func TestUpsertCreateBranchHookWritesASystemField(t *testing.T) {
	forEachSystemAuthorProvider(t, false, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		ctx := context.Background()
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 2})))
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(81), systemAuthorPost(fixture, 81, nil, "created"), fixture.updateTitle("unused")); err != nil {
			t.Fatalf("hook-authored system write on the upsert create branch was refused: %v: %v", err, errorChain(err))
		}
		if author, _ := readPostAuthor(t, fixture, 81); author != mutationResultUUIDText(3) {
			t.Fatalf("persisted author=%s, want the value the create hook authored", author)
		}
	})
}

func TestUpsertUpdateBranchHookWritesASystemField(t *testing.T) {
	forEachSystemAuthorProvider(t, false, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		ctx := context.Background()
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 2})))
		seedSystemAuthorVocabularyPost(t, fixture, 82, golem.UUID{15: 1})
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(82), systemAuthorPost(fixture, 82, nil, "unused"), fixture.updateTitle("caller wrote this")); err != nil {
			t.Fatalf("hook-authored system write on the upsert update branch was refused: %v: %v", err, errorChain(err))
		}
		if author, _ := readPostAuthor(t, fixture, 82); author != mutationResultUUIDText(2) {
			t.Fatalf("persisted author=%s, want the value the update hook authored", author)
		}
	})
}

func TestUpsertUpdateBranchCallerSystemFieldStaysRefusedWhenAHookRewritesIt(t *testing.T) {
	forEachSystemAuthorProvider(t, false, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		ctx := context.Background()
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 3})))
		seedSystemAuthorVocabularyPost(t, fixture, 83, golem.UUID{15: 1})
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		forged := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}),
		)
		_, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(83), systemAuthorPost(fixture, 83, nil, "unused"), forged)
		requireRefusal(t, err, "system field is not caller writable", "a hook rewriting the caller's own system-field value laundered it into an upsert update")
		if author, _ := readPostAuthor(t, fixture, 83); author != mutationResultUUIDText(1) {
			t.Fatalf("persisted author=%s, want the seeded value", author)
		}
	})
}

func TestUpsertCreateBranchCallerSystemFieldStaysRefusedWhenAHookRewritesIt(t *testing.T) {
	forEachSystemAuthorProvider(t, false, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		ctx := context.Background()
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 3})))
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		bob := golem.UUID{15: 2}
		_, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(84), systemAuthorPost(fixture, 84, &bob, "forged"), fixture.updateTitle("unused"))
		requireRefusal(t, err, "system field is not caller writable", "a hook rewriting the caller's own system-field value laundered it into an upsert create")
		if count := countPosts(t, fixture.mutationResultFixture, 84); count != 0 {
			t.Fatalf("refused upsert create persisted %d rows", count)
		}
	})
}

func TestVersionedUpdateHookWritesASystemField(t *testing.T) {
	forEachSystemAuthorProvider(t, true, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		ctx := context.Background()
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 2})))
		seedSystemAuthorVocabularyPost(t, fixture, 91, golem.UUID{15: 1})
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpdateVersioned(ctx, caller, fixture.postDescriptor, fixture.target(91), golem.ExpectVersion(1), fixture.updateTitle("caller wrote this")); err != nil {
			t.Fatalf("hook-authored system write on a versioned update was refused: %v: %v", err, errorChain(err))
		}
		if author, version := readPostAuthor(t, fixture, 91); author != mutationResultUUIDText(2) || version != 2 {
			t.Fatalf("persisted author=%s version=%d, want the hook's author at version 2", author, version)
		}
	})
}

func TestVersionedCallerSystemFieldStaysRefusedWhenAHookRewritesIt(t *testing.T) {
	forEachSystemAuthorProvider(t, true, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		ctx := context.Background()
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, systemAuthorHooks(hookTitleAndAuthor(golem.UUID{15: 3})))
		seedSystemAuthorVocabularyPost(t, fixture, 94, golem.UUID{15: 1})
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		forged := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}),
		)
		_, err := CallerUpdateVersioned(ctx, caller, fixture.postDescriptor, fixture.target(94), golem.ExpectVersion(1), forged)
		requireRefusal(t, err, "system field is not caller writable", "a hook rewriting the caller's own system-field value laundered it into a versioned update")
		_, err = CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(94), golem.ExpectExisting(1), systemAuthorPost(fixture, 94, nil, "unused"), forged)
		requireRefusal(t, err, "system field is not caller writable", "a hook rewriting the caller's own system-field value laundered it into a versioned upsert update")
		if author, version := readPostAuthor(t, fixture, 94); author != mutationResultUUIDText(1) || version != 1 {
			t.Fatalf("persisted author=%s version=%d, want the seeded row unchanged", author, version)
		}
	})
}

func executorCreateHooks(createAuthor *golem.UUID, inner *error, invoke func(context.Context, golem.HookExecutor, mutationVocabularyFixture) error) func(mutationVocabularyFixture) []golem.HookBinding[mutationResultActor] {
	return func(fixture mutationVocabularyFixture) []golem.HookBinding[mutationResultActor] {
		capability := golem.GeneratedCreateFieldCapability(fixture.schema.Post, fixture.authorID)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
				if createAuthor == nil {
					return nil
				}
				return golem.SetCreate(request, capability, *createAuthor)
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookResult[mutationResultUser]](fixture.schema.User, golem.HookUpdate, func(hookContext context.Context, result golem.UpdateHookResult[mutationResultUser]) error {
				*inner = invoke(hookContext, result.Executor(), fixture)
				return *inner
			}),
		}
	}
}

func triggerVocabularyHookExecutor(t *testing.T, fixture mutationVocabularyFixture) error {
	t.Helper()
	caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
	target := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
	_, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, target, golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "trigger")))
	return err
}

func TestHookExecutorCreateHookSuppliesARequiredSystemField(t *testing.T) {
	forEachSystemAuthorProvider(t, false, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		carol := golem.UUID{15: 3}
		var inner error
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, executorCreateHooks(&carol, &inner, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationVocabularyFixture) error {
			_, err := golem.HookCreateRow(hookContext, executor, fixture.postDescriptor, systemAuthorPost(fixture, 121, nil, "created"))
			return err
		}))
		if err := triggerVocabularyHookExecutor(t, fixture); err != nil {
			t.Fatalf("hook-supplied required system field was refused through HookCreateRow: %v: %v", err, errorChain(inner))
		}
		if author, _ := readPostAuthor(t, fixture, 121); author != mutationResultUUIDText(3) {
			t.Fatalf("persisted author=%s, want the value the create hook supplied", author)
		}
	})
}

func TestHookExecutorCreateRefusesWhenTheHookOmitsARequiredSystemField(t *testing.T) {
	forEachSystemAuthorProvider(t, false, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		var inner error
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, executorCreateHooks(nil, &inner, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationVocabularyFixture) error {
			_, err := golem.HookCreateRow(hookContext, executor, fixture.postDescriptor, systemAuthorPost(fixture, 122, nil, "omitted"))
			return err
		}))
		if err := triggerVocabularyHookExecutor(t, fixture); err == nil {
			t.Fatal("HookCreateRow created a row whose required system field no one supplied")
		}
		requireRefusal(t, inner, "required create field is absent", "HookCreateRow omission refusal")
		if count := countPosts(t, fixture.mutationResultFixture, 122); count != 0 {
			t.Fatalf("refused HookCreateRow persisted %d rows", count)
		}
	})
}

func TestHookExecutorCreateCallerSystemFieldStaysRefusedWhenAHookRewritesIt(t *testing.T) {
	forEachSystemAuthorProvider(t, false, func(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture) {
		bob, carol := golem.UUID{15: 2}, golem.UUID{15: 3}
		var inner error
		fixture := openSystemAuthorVocabularyFixture(t, database, provider, schema, true, executorCreateHooks(&carol, &inner, func(hookContext context.Context, executor golem.HookExecutor, fixture mutationVocabularyFixture) error {
			_, err := golem.HookCreateRow(hookContext, executor, fixture.postDescriptor, systemAuthorPost(fixture, 123, &bob, "forged"))
			return err
		}))
		if err := triggerVocabularyHookExecutor(t, fixture); err == nil {
			t.Fatal("a hook rewriting the executor caller's own system-field value laundered it into a create")
		}
		requireRefusal(t, inner, "system field is not caller writable", "HookCreateRow third-value refusal")
		if count := countPosts(t, fixture.mutationResultFixture, 123); count != 0 {
			t.Fatalf("refused HookCreateRow persisted %d rows", count)
		}
	})
}
