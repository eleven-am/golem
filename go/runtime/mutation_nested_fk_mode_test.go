package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type foreignKeyModeCase struct {
	name     string
	create   bool
	nullable bool
	seed     func(*testing.T, nestedSystemFieldFixture)
	write    func(*Caller[mutationResultPrincipal, mutationResultActor], nestedSystemFieldFixture) error
	direct   func(*Caller[mutationResultPrincipal, mutationResultActor], nestedSystemFieldFixture) error
	assert   func(*testing.T, nestedSystemFieldFixture, bool)
}

var foreignKeyModeReasons = map[compilerir.FieldMode]string{
	compilerir.ModeSystem:    "system field is not caller writable",
	compilerir.ModeReadOnly:  "read-only field is not writable",
	compilerir.ModeImmutable: "immutable field is writable only during create",
	compilerir.ModeHidden:    "hidden field is not writable",
}

func systemAuthorID(fixture schematest.Fixture) (golem.ModelID, golem.FieldID) {
	return fixture.Post, fixture.AuthorID
}

func (fixture nestedSystemFieldFixture) aliceTarget() golem.MutationTarget[mutationResultUser] {
	return fixture.alice()
}

func (fixture nestedSystemFieldFixture) postAuthor(t *testing.T, id byte) (string, bool) {
	t.Helper()
	var author *string
	query := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id" = ?`)
	if err := fixture.app.database.QueryRowxContext(context.Background(), query, mutationResultUUIDText(id)).Scan(&author); err != nil {
		t.Fatal(err)
	}
	if author == nil {
		return "", false
	}
	return strings.ToLower(*author), true
}

func (fixture nestedSystemFieldFixture) assertPostAuthor(t *testing.T, id byte, want byte) {
	t.Helper()
	if author, ok := fixture.postAuthor(t, id); !ok || author != mutationResultUUIDText(want) {
		t.Fatalf("post %d author=%q present=%t, want %s", id, author, ok, mutationResultUUIDText(want))
	}
}

func (fixture nestedSystemFieldFixture) createPostInput(id byte, extra ...golem.CreateValue[mutationResultPost]) golem.CreateInput[mutationResultPost] {
	decimal, _ := golem.ParseDecimal("1.25")
	values := []golem.CreateValue[mutationResultPost]{
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "created"),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.bigInt, int64(10)),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.decimal, decimal),
	}
	return golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post, append(values, extra...)...)
}

func foreignKeyModeCases() []foreignKeyModeCase {
	directUpdate := func(caller *Caller[mutationResultPrincipal, mutationResultActor], fixture nestedSystemFieldFixture) error {
		_, err := CallerUpdate(context.Background(), caller, fixture.postDescriptor, fixture.target(110), golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1})))
		return err
	}
	directCreate := func(caller *Caller[mutationResultPrincipal, mutationResultActor], fixture nestedSystemFieldFixture) error {
		_, err := CallerCreate(context.Background(), caller, fixture.postDescriptor, fixture.createPostInput(119, golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1})))
		return err
	}
	unchangedBob := func(t *testing.T, fixture nestedSystemFieldFixture, refused bool) {
		if refused {
			fixture.assertPostAuthor(t, 110, 2)
			return
		}
		fixture.assertPostAuthor(t, 110, 1)
	}
	seedBob := func(t *testing.T, fixture nestedSystemFieldFixture) { fixture.seedPost(t, 110, 2, "seeded", 5) }
	createdOrAbsent := func(id byte) func(*testing.T, nestedSystemFieldFixture, bool) {
		return func(t *testing.T, fixture nestedSystemFieldFixture, refused bool) {
			if refused {
				if count := fixture.countRows(t, fixture.schema.Post, id); count != 0 {
					t.Fatalf("refused create left %d post rows", count)
				}
				return
			}
			fixture.assertPostAuthor(t, id, 1)
		}
	}
	return []foreignKeyModeCase{
		{name: "inverse-connect", seed: seedBob, direct: directUpdate, assert: unchangedBob,
			write: func(caller *Caller[mutationResultPrincipal, mutationResultActor], fixture nestedSystemFieldFixture) error {
				_, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.aliceTarget(), fixture.nestedPostConnect(110))
				return err
			}},
		{name: "inverse-set", seed: seedBob, direct: directUpdate, assert: unchangedBob,
			write: func(caller *Caller[mutationResultPrincipal, mutationResultActor], fixture nestedSystemFieldFixture) error {
				input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
					golem.GeneratedNestedSet[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.target(110)))
				_, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.aliceTarget(), input)
				return err
			}},
		{name: "inverse-disconnect", nullable: true, direct: directUpdate,
			seed: func(t *testing.T, fixture nestedSystemFieldFixture) { fixture.seedPost(t, 110, 1, "seeded", 5) },
			write: func(caller *Caller[mutationResultPrincipal, mutationResultActor], fixture nestedSystemFieldFixture) error {
				input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
					golem.GeneratedNestedDisconnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.target(110)))
				_, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.aliceTarget(), input)
				return err
			},
			assert: func(t *testing.T, fixture nestedSystemFieldFixture, refused bool) {
				author, present := fixture.postAuthor(t, 110)
				if refused != present || refused && author != mutationResultUUIDText(1) {
					t.Fatalf("post author=%q present=%t after refused=%t disconnect", author, present, refused)
				}
			}},
		{name: "source-connect-on-update", seed: seedBob, direct: directUpdate, assert: unchangedBob,
			write: func(caller *Caller[mutationResultPrincipal, mutationResultActor], fixture nestedSystemFieldFixture) error {
				input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
					golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, fixture.aliceTarget()))
				_, err := CallerUpdate(context.Background(), caller, fixture.postDescriptor, fixture.target(110), input)
				return err
			}},
		{name: "nested-create-correlation", create: true, direct: directCreate, assert: createdOrAbsent(111),
			write: func(caller *Caller[mutationResultPrincipal, mutationResultActor], fixture nestedSystemFieldFixture) error {
				input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
					golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.createPostInput(111)))
				_, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.aliceTarget(), input)
				return err
			}},
		{name: "source-connect-on-create", create: true, direct: directCreate, assert: createdOrAbsent(112),
			write: func(caller *Caller[mutationResultPrincipal, mutationResultActor], fixture nestedSystemFieldFixture) error {
				input := fixture.createPostInput(112, golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, fixture.aliceTarget()))
				_, err := CallerCreate(context.Background(), caller, fixture.postDescriptor, input)
				return err
			}},
	}
}

func passthroughPostHooks(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string], _ golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor] {
	return []golem.HookBinding[mutationResultActor]{
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(context.Context, *golem.UpdateHookRequest[mutationResultPost]) error { return nil }),
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[mutationResultPost]) error { return nil }),
	}
}

func publicErrorCode(err error) golem.ErrorCode {
	var public *golem.Error
	if errors.As(err, &public) {
		return public.Code
	}
	return ""
}

func TestCallerRelationWritesObeyTheForeignKeyScalarMode(t *testing.T) {
	for _, mode := range []compilerir.FieldMode{compilerir.ModeSystem, compilerir.ModeReadOnly, compilerir.ModeImmutable, compilerir.ModeHidden} {
		mode := mode
		for _, relationCase := range foreignKeyModeCases() {
			relationCase := relationCase
			refused := !(mode == compilerir.ModeImmutable && relationCase.create)
			for _, hooked := range []bool{false, true} {
				hooked := hooked
				variant := "no-hook"
				var hooks nestedSystemFieldHooks
				if hooked {
					variant, hooks = "do-nothing-hook", passthroughPostHooks
				}
				t.Run(string(mode)+"/"+relationCase.name+"/"+variant, func(t *testing.T) {
					spec := nestedSystemFieldSpec{field: systemAuthorID, modes: []compilerir.FieldMode{mode}, hooks: hooks}
					if relationCase.nullable {
						spec.prepare = func(t testing.TB, fixture schematest.Fixture) schematest.Fixture {
							return withNullableScalar(t, fixture, fixture.AuthorID)
						}
					}
					forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
						if relationCase.seed != nil {
							relationCase.seed(t, fixture)
						}
						caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
						err := relationCase.write(caller, fixture)
						if !refused {
							if err != nil {
								t.Fatalf("immutable foreign key at create was refused like an update: %v: %v", err, errorChain(err))
							}
							relationCase.assert(t, fixture, false)
							return
						}
						reason := foreignKeyModeReasons[mode]
						if err == nil || !strings.Contains(errorChain(err), reason) {
							t.Fatalf("relation write over a %s foreign key was not refused with %q: %v", mode, reason, err)
						}
						relationCase.assert(t, fixture, true)
						direct := relationCase.direct(caller, fixture)
						if direct == nil || !strings.Contains(errorChain(direct), reason) {
							t.Fatalf("direct write reference did not refuse with %q: %v", reason, direct)
						}
						if publicErrorCode(err) != publicErrorCode(direct) || errors.Unwrap(err) == nil {
							t.Fatalf("relation refusal code=%q differs from direct refusal code=%q", publicErrorCode(err), publicErrorCode(direct))
						}
					})
				})
			}
		}
	}
}

func TestSystemClientRelationWriteMayAssignASystemForeignKey(t *testing.T) {
	forEachNestedSystemFieldProvider(t, nestedSystemFieldSpec{field: systemAuthorID, modes: []compilerir.FieldMode{compilerir.ModeSystem}}, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 110, 2, "seeded", 5)
		if _, err := SystemUpdate(context.Background(), fixture.app.System(), fixture.userDescriptor, fixture.aliceTarget(), fixture.nestedPostConnect(110)); err != nil {
			t.Fatalf("system client connect over a system foreign key was refused: %v: %v", err, errorChain(err))
		}
		fixture.assertPostAuthor(t, 110, 1)
	})
}

func TestNestedChildHookMayAuthorASystemForeignKey(t *testing.T) {
	hooks := func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string], _ golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor] {
		author := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, title, "hook kept this"),
					golem.GeneratedSetFieldValue(schema.Post, author, golem.UUID{15: 2}),
				))
				return nil
			}),
		}
	}
	forEachNestedSystemFieldProvider(t, nestedSystemFieldSpec{field: systemAuthorID, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: hooks}, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 113, 1, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		input := fixture.nestedPostUpdate(113, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"))
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.aliceTarget(), input); err != nil {
			t.Fatalf("nested child hook-authored system foreign key was refused: %v: %v", err, errorChain(err))
		}
		fixture.assertPostAuthor(t, 113, 2)
	})
}

func TestSystemClientRelationWriteRefusesAHiddenForeignKeyLikeADirectWrite(t *testing.T) {
	forEachNestedSystemFieldProvider(t, nestedSystemFieldSpec{field: systemAuthorID, modes: []compilerir.FieldMode{compilerir.ModeHidden}}, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 110, 2, "seeded", 5)
		ctx := context.Background()
		_, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.aliceTarget(), fixture.nestedPostConnect(110))
		if err == nil || !strings.Contains(errorChain(err), "hidden field is not writable") {
			t.Fatalf("system client connect over a hidden foreign key was not refused: %v", err)
		}
		fixture.assertPostAuthor(t, 110, 2)
		_, direct := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(110), golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1})))
		if direct == nil || !strings.Contains(errorChain(direct), "hidden field is not writable") || publicErrorCode(direct) != publicErrorCode(err) {
			t.Fatalf("direct system write reference=%v code=%q, relation code=%q", direct, publicErrorCode(direct), publicErrorCode(err))
		}
		fixture.assertPostAuthor(t, 110, 2)
	})
}
